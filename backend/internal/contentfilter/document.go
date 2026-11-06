package contentfilter

// 内容过滤结构化解析第五步 a:把启用的规则集合并成给 App 的快照文档(纯函数,5b 再接进 Service)。
//
// 对齐上游 v0.1.402(v0.1.410 核对过无变化;只读参考 catch-up-upstream 的面板服务端,代码全部自写):
//
//	文档  {version:1, hostnames:[], rules:[], scripts:{<sha256>:<脚本正文>}}
//	元数据 {version:1, sha256, size, hostnames, sets:[{id,name,dialect,hostnames,ruleCount,scriptCount}]}
//
// sha256 / size 是文档 JSON 正文(就是 App 拉到的那串字节)的摘要和字节数。规则 id 在文档里写成
// `<集合id>:<规则id>` 并带 `ruleSetId`;同一个 id 再出现时加 `-2`、`-3`…;主机名跨集合去重(小写、去末尾的点);
// 脚本按内容 sha256 去重存放。上限:5000 条规则、2000 个主机名、正文 2 MiB,超了整份拒绝、不截断。

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 文档的上限。
const (
	MaxDocRules     = 5000
	MaxDocHostnames = 2000
	MaxDocBytes     = 2 << 20
)

// ErrDocTooLarge 表示启用的规则合起来超过上限。
var ErrDocTooLarge = errors.New("内容过滤规则过大,请减少启用的规则集(最多 2 MiB)")

// SetData 是合并用的一份规则集:已经解析好的结构化数据(不是原文)。
type SetData struct {
	ID        string
	Name      string
	Dialect   Dialect
	Hostnames []string
	Rules     []Rule
	Scripts   map[string]string // sha256 → 脚本正文
}

// ScriptCount 是这份规则集里 script 规则的条数。
func (s SetData) ScriptCount() int {
	n := 0
	for _, r := range s.Rules {
		if r.Action == "script" {
			n++
		}
	}
	return n
}

// Document 是快照文档。
type Document struct {
	Version   int               `json:"version"`
	Hostnames []string          `json:"hostnames"`
	Rules     []Rule            `json:"rules"`
	Scripts   map[string]string `json:"scripts"`
}

// SetMeta 是元数据里一份集合的摘要。
type SetMeta struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Dialect     Dialect  `json:"dialect"`
	Hostnames   []string `json:"hostnames"`
	RuleCount   int      `json:"ruleCount"`
	ScriptCount int      `json:"scriptCount"`
}

// Metadata 是快照的元数据(App 先拉它,sha256 变了才去拉正文)。
type Metadata struct {
	Version   int       `json:"version"`
	SHA256    string    `json:"sha256"`
	Size      int       `json:"size"`
	Hostnames []string  `json:"hostnames"`
	Sets      []SetMeta `json:"sets"`
}

// Built 是合并的结果:文档、正文字节、元数据。
type Built struct {
	Document Document
	Body     []byte
	Metadata Metadata
}

func normHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(trimJS(h)), ".")
}

// BuildDocument 把传进来的集合(调用方只传启用的、并按稳定顺序排好)合并成文档。
func BuildDocument(sets []SetData) (*Built, error) {
	doc := Document{Version: 1, Hostnames: []string{}, Rules: []Rule{}, Scripts: map[string]string{}}
	seenHost := map[string]bool{}
	usedID := map[string]bool{}
	meta := Metadata{Version: 1, Hostnames: []string{}, Sets: []SetMeta{}}
	for _, s := range sets {
		for _, h := range s.Hostnames {
			if h = normHost(h); h != "" && !seenHost[h] {
				seenHost[h] = true
				doc.Hostnames = append(doc.Hostnames, h)
			}
		}
		for _, r := range s.Rules {
			base := s.ID + ":" + r.ID
			id, n := base, 2
			for usedID[id] {
				id = base + "-" + strconv.Itoa(n)
				n++
			}
			usedID[id] = true
			r.ID, r.RuleSetID = id, s.ID
			doc.Rules = append(doc.Rules, r)
		}
		for sha, text := range s.Scripts {
			doc.Scripts[sha] = text
		}
		hs := s.Hostnames
		if hs == nil {
			hs = []string{}
		}
		meta.Sets = append(meta.Sets, SetMeta{ID: s.ID, Name: s.Name, Dialect: s.Dialect, Hostnames: hs,
			RuleCount: len(s.Rules), ScriptCount: s.ScriptCount()})
	}
	if len(doc.Rules) > MaxDocRules || len(doc.Hostnames) > MaxDocHostnames {
		return nil, ErrDocTooLarge
	}
	body, err := marshalNoEscape(doc)
	if err != nil {
		return nil, fmt.Errorf("生成内容过滤快照失败: %w", err)
	}
	if len(body) > MaxDocBytes {
		return nil, ErrDocTooLarge
	}
	sum := sha256.Sum256(body)
	meta.SHA256, meta.Size, meta.Hostnames = hex.EncodeToString(sum[:]), len(body), doc.Hostnames
	return &Built{Document: doc, Body: body, Metadata: meta}, nil
}

// marshalNoEscape 与 JSON.stringify 一样不把 < > & 转成 \u00xx。
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
