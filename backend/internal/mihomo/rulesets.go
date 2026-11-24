package mihomo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// 规则集转 Mihomo provider(双内核 3e-3;对应上游 v0.1.462 Node 的 PE 和 ze / ar)。
//
// Mihomo 读不了 sing-box 的 .srs 二进制规则集,所以每个站点集用到的规则集(随包 geosite-* / geoip-*,
// 用户规则集链接编出的 list-<8 位十六进制>[-ip])先用 `sing-box rule-set decompile` 还原成源文件 JSON,
// 再由 BuildProvider 写成 Mihomo 的 yaml provider,放在 <DataDir>/mihomo/rules/ 下。
// 只有这两大类名字能转;别的名字整体报错(Mihomo 表达不了的整体报错)。

var (
	ruleSetNameRe = regexp.MustCompile(`^[A-Za-z0-9._!@-]+$`)
	listTagRe     = regexp.MustCompile(`^list-[a-f0-9]{8}$`)
	listIPTagRe   = regexp.MustCompile(`^list-[a-f0-9]{8}-ip$`)
	geoPrefixRe   = regexp.MustCompile(`^(geoip|geosite)-.+`)
)

// ValidRuleSetName 对应上游 vr:规则集名只允许字母数字和 `._!@-`,且不含 `..`(防路径穿越)。
func ValidRuleSetName(tag string) bool {
	return ruleSetNameRe.MatchString(tag) && !strings.Contains(tag, "..")
}

// geoKind 对应上游 ar:随包规则库的名字返回 "geoip" / "geosite",其余(含不合法的名字)返回空。
func geoKind(tag string) string {
	if !ValidRuleSetName(tag) {
		return ""
	}
	if m := geoPrefixRe.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return ""
}

// RuleSetKind 返回规则集在 Mihomo 里的类别:"ipcidr"(geoip-*、list-xxxxxxxx-ip)或 "domain"(geosite-*、list-xxxxxxxx)。
// 别的名字返回 ok=false。
func RuleSetKind(tag string) (kind string, ok bool) {
	g := geoKind(tag)
	switch {
	case g == "geoip" || listIPTagRe.MatchString(tag):
		return "ipcidr", true
	case g == "geosite" || listTagRe.MatchString(tag):
		return "domain", true
	}
	return "", false
}

// RuleSetPathFunc 对应上游 ze:随包规则库(geoip-* / geosite-*)在 geoDir,用户规则集在 rulesetDir,文件名 <tag>.srs。
func RuleSetPathFunc(geoDir, rulesetDir string) func(tag string) (string, error) {
	return func(tag string) (string, error) {
		if !ValidRuleSetName(tag) {
			return "", fmt.Errorf("不合法的规则集名 %s", tag)
		}
		dir := rulesetDir
		if geoKind(tag) != "" {
			dir = geoDir
		}
		return filepath.Join(dir, tag+".srs"), nil
	}
}

// SingBoxDecompiler 返回用 sing-box 可执行文件还原 .srs 的函数:`sing-box rule-set decompile --output <out> <srs>`。
// 超时 60 秒;失败只回固定文案,不带进程输出(上游同样只报「无法读取现有规则集文件」)。
func SingBoxDecompiler(singbox string) func(ctx context.Context, srs, out string) error {
	return func(ctx context.Context, srs, out string) error {
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, singbox, "rule-set", "decompile", "--output", out, srs)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		cmd.WaitDelay = 2 * time.Second
		if err := cmd.Run(); err != nil {
			return errors.New("无法读取现有规则集文件")
		}
		return nil
	}
}

// Converter 把规则集转成 provider。零值不可用,字段都要填。
type Converter struct {
	FS FS
	// RulesDir 是写 provider 的目录:<DataDir>/mihomo/rules。
	RulesDir string
	// SrsPath 给出规则集名对应的 .srs 路径(RuleSetPathFunc)。
	SrsPath func(tag string) (string, error)
	// Decompile 把 .srs 还原成源文件 JSON 写到 out(SingBoxDecompiler)。
	Decompile func(ctx context.Context, srs, out string) error
}

// Converted 是 Convert 的结果:每个规则集名对应的 provider,和它的类别(domain / ipcidr)。
type Converted struct {
	Providers map[string]Provider
	Kinds     map[string]string
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Convert 依次转换 tags(重复的只转一次,按首次出现的顺序)。创建 RulesDir;
// 不认得的名字报「mihomo 不支持规则集 X」,转换失败报「mihomo 规则集 X 转换失败:原因」;出错即停。
// 还原用的临时 JSON 放在 RulesDir 下的隐藏文件里,无论成败都会删掉。
func (c *Converter) Convert(ctx context.Context, tags []string) (*Converted, error) {
	out := &Converted{Providers: map[string]Provider{}, Kinds: map[string]string{}}
	if err := c.FS.MkdirAll(c.RulesDir); err != nil {
		return nil, err
	}
	for _, tag := range tags {
		if _, done := out.Kinds[tag]; done {
			continue
		}
		kind, ok := RuleSetKind(tag)
		if !ok {
			return nil, fmt.Errorf("mihomo 不支持规则集 %s", tag)
		}
		p, err := c.convertOne(ctx, tag, kind)
		if err != nil {
			return nil, fmt.Errorf("mihomo 规则集 %s 转换失败：%w", tag, err)
		}
		out.Providers[tag] = p
		out.Kinds[tag] = kind
	}
	return out, nil
}

func (c *Converter) convertOne(ctx context.Context, tag, kind string) (Provider, error) {
	srs, err := c.SrsPath(tag)
	if err != nil {
		return Provider{}, err
	}
	tmp := filepath.Join(c.RulesDir, "."+randomHex(16)+".json")
	defer func() { _ = c.FS.Remove(tmp) }()
	if err := c.Decompile(ctx, srs, tmp); err != nil {
		return Provider{}, err
	}
	raw, err := c.FS.ReadFile(tmp)
	if err != nil {
		return Provider{}, err
	}
	src, err := ParseSource(raw)
	if err != nil {
		return Provider{}, err
	}
	return WriteProvider(c.FS, c.RulesDir, tag, src, kind)
}
