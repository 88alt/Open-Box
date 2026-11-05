package clientapp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/timeflysoon/Open-Box/backend/internal/geodata"
)

// config/:token 下面的子资源(对齐上游 v0.1.402):规则集文件、面板背景图。
// 设备校验和 config/:token 共用 authDevice:设备不认得回 ErrPairUnpaired,同步开关关着回 ErrPairDisabled,
// 找不到的资源回 ErrTokenNotFound(404 not found)。

// backgroundFile 是 /api/background-image 存背景图的文件名(internal/api/storage 的 imageName)。
const backgroundFile = "background-image"

// maxBackgroundBytes 是背景图解码后大小的上限(PUT 的上限是 12 MiB 的 data URL,这里放宽到同一量级的解码结果)。
const maxBackgroundBytes = 12 << 20

// 用户规则集链接编出的本地规则集名:list-<8 位十六进制>,IP 那份加 -ip(算法见 policyentries.ListTagForURL)。
var listTagRe = regexp.MustCompile(`^list-[0-9a-f]{8}(-ip)?$`)

// RuleSetPath 返回 App 要的规则集文件路径。tag 可带 .srs 后缀;只认两类名字:随包规则库的 geosite-* / geoip-*(从 GeoDir 取)
// 和用户规则集链接编出的 list-<hash>(从 RulesetDir 取),别的名字(含路径穿越)一律 ErrTokenNotFound。
// 只回普通文件,目录、符号链接指向的目录不算。
func (s *Service) RuleSetPath(ctx context.Context, routerID, token, tag string) (string, error) {
	if _, _, err := s.authDevice(ctx, routerID, token); err != nil {
		return "", err
	}
	tag = strings.TrimSuffix(tag, ".srs")
	var dir string
	switch {
	case geodata.ValidTag(tag):
		dir = s.GeoDir
	case listTagRe.MatchString(tag):
		dir = s.RulesetDir
	}
	if dir == "" {
		return "", ErrTokenNotFound
	}
	path := filepath.Join(dir, tag+".srs")
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", ErrTokenNotFound
	}
	return path, nil
}

// BackgroundImage 是面板背景图的原始字节。Version 是内容的短摘要,App 拿它判断本地缓存要不要换。
type BackgroundImage struct {
	Type    string
	Bytes   []byte
	Version string
}

// Background 取面板背景图(/api/background-image 存的那一张)。没设置、存的不是合法的 data:image/ 图片,回 ErrTokenNotFound。
func (s *Service) Background(ctx context.Context, routerID, token string) (*BackgroundImage, error) {
	if _, _, err := s.authDevice(ctx, routerID, token); err != nil {
		return nil, err
	}
	var f struct {
		Image string `json:"image"`
	}
	if _, err := s.Store.Load(backgroundFile, &f); err != nil {
		return nil, err
	}
	typ, data, ok := parseImageDataURL(f.Image)
	if !ok {
		return nil, ErrTokenNotFound
	}
	sum := sha256.Sum256(data)
	return &BackgroundImage{Type: typ, Bytes: data, Version: hex.EncodeToString(sum[:8])}, nil
}

// parseImageDataURL 拆 data:image/<子类型>[;参数];base64,<内容>。子类型只许字母数字和 + . -,免得把奇怪的类型写进响应头。
func parseImageDataURL(u string) (typ string, data []byte, ok bool) {
	rest, found := strings.CutPrefix(u, "data:image/")
	if !found {
		return "", nil, false
	}
	meta, payload, found := strings.Cut(rest, ",")
	if !found {
		return "", nil, false
	}
	parts := strings.Split(meta, ";")
	if len(parts) < 2 || parts[len(parts)-1] != "base64" {
		return "", nil, false
	}
	sub := parts[0]
	if sub == "" || len(sub) > 32 {
		return "", nil, false
	}
	for _, c := range sub {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '.' || c == '-') {
			return "", nil, false
		}
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > maxBackgroundBytes+3 {
		return "", nil, false
	}
	b, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(b) == 0 || len(b) > maxBackgroundBytes {
		return "", nil, false
	}
	return "image/" + strings.ToLower(sub), b, true
}
