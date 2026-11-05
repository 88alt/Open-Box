package clientapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// POST /api/openbox/client-config/file:导出文件。把本地分流的配置用配对码封好,连同「导入码」一起给面板存成 .json。
// 自己的设计,依据前端 api/clientApp.ts 的注释(服务端用配对码封好的整份配置、含 importCode、lan / publicBase
// 是码里的两项)和 helper/clientAppCode.ts 里 import 码的字段顺序,封包格式见 seal.go;没有参照上游。
//
// 导出不看同步开关:开关管的是 App 用的公开接口,文件是用户手动拿走的。
// 导出文件等于凭据(配对码只有 40 位,文件被拿走后可离线爆破,60 万次 PBKDF2 只是拖慢),面板的响应是 no-store。

const (
	// BundleFormat 是导出文件里 format 字段的值。
	BundleFormat = "open-box-client-config"
	// BundleVersion 是导出文件本身的版本(封包另有自己的 v)。
	BundleVersion = 1

	maxLanLen        = 253
	maxPublicBaseLen = 2048
)

// Bundle 是导出文件的内容。前端把它当 Record<string, unknown> 原样存成 JSON,所以字段由我们定:
// 不封的部分只有识别用的(格式、版本、路由器 ID / 名字、导入码),真正的配置全在 seal 里。
type Bundle struct {
	Format     string   `json:"format"`
	V          int      `json:"v"`
	RouterID   string   `json:"routerId"`
	RouterName string   `json:"routerName"`
	ImportCode string   `json:"importCode"`
	ExportedAt int64    `json:"exportedAt"`
	Seal       Envelope `json:"seal"`
}

// payload 是封进去的明文:App 导入后要用的整份本地分流配置。
// shareRegions 是此刻生效的地区分流(没改过 = 默认三组)。App 发给内核时每组最前面要自己带上
// 「lanSubnets 这些网段 → 回路由器」(前端对 lanSubnets 的说明),所以这里只给网段、不改每组的规则。
type payload struct {
	V            int        `json:"v"`
	Kind         string     `json:"kind"`
	GeneratedAt  int64      `json:"generatedAt"`
	Router       payloadRtr `json:"router"`
	ShareRegions []Group    `json:"shareRegions"`
}

type payloadRtr struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	LanAddresses []string   `json:"lanAddresses"`
	LanSubnets   []string   `json:"lanSubnets"`
	PanelPort    int        `json:"panelPort"`
	ServerInfo   ServerInfo `json:"serverInfo"`
}

// InputError 是调用方传的参数不合格(接口回 400)。
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func (s *Service) kdfIter() int {
	if s.KDFIter > 0 {
		return s.KDFIter
	}
	return DefaultSealIter
}

// ExportFile 生成导出文件。lan / publicBase 是导入码里的 lan 和 pub 两项,为空就不带(lan 空时码里仍带 lan=,
// 与前端 buildClientImportCode 一致)。参数不合格返回 *InputError,什么都不写。
func (s *Service) ExportFile(ctx context.Context, lan, publicBase string) (Bundle, error) {
	lan, publicBase = strings.TrimSpace(lan), strings.TrimSpace(publicBase)
	if err := checkLan(lan); err != nil {
		return Bundle{}, err
	}
	if err := checkPublicBase(publicBase); err != nil {
		return Bundle{}, err
	}
	is, err := s.ClientConfig(ctx) // 第一次会补齐并保存配对码和 nonce
	if err != nil {
		return Bundle{}, err
	}
	env, now, err := s.sealConfig(ctx, is)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		Format: BundleFormat, V: BundleVersion,
		RouterID: is.RouterID, RouterName: is.RouterName,
		ImportCode: ImportCode(is.RouterID, is.RouterName, lan, is.PanelPort, publicBase, is.Nonce),
		ExportedAt: now, Seal: env,
	}, nil
}

// sealConfig 把此刻的本地分流配置按 is 里的配对码封好。导出文件和公开同步接口共用,返回封包和生成时刻(毫秒)。
func (s *Service) sealConfig(ctx context.Context, is Issue) (Envelope, int64, error) {
	info, err := s.Info(ctx)
	if err != nil {
		return Envelope{}, 0, err
	}
	now := s.now().UnixMilli()
	plain, err := json.Marshal(payload{
		V: 1, Kind: "client-config", GeneratedAt: now,
		Router: payloadRtr{
			ID: info.RouterID, Name: info.RouterName, LanAddresses: info.LanAddresses,
			LanSubnets: info.LanSubnets, PanelPort: info.PanelPort, ServerInfo: info.ServerInfo,
		},
		ShareRegions: info.ShareRegions,
	})
	if err != nil {
		return Envelope{}, 0, fmt.Errorf("编码配置失败: %w", err)
	}
	iter := s.kdfIter()
	key, err := s.keys.get(is.PairCode, sealSalt(is.RouterID, is.Nonce), iter)
	if err != nil {
		return Envelope{}, 0, err
	}
	env, err := seal(key, is.RouterID, is.Nonce, iter, plain, s.Rand)
	if err != nil {
		return Envelope{}, 0, fmt.Errorf("封包失败: %w", err)
	}
	return env, now, nil
}

// ImportCode 拼本地分流的导入码,字段顺序和写法与前端 helper/clientAppCode.ts 的 buildClientImportCode 一致:
// openbox://import?v=2&r=路由器ID&n=路由器名&lan=…&port=…[&pub=…]&p=nonce,各值按 encodeURIComponent 转义。
func ImportCode(routerID, routerName, lan string, port int, publicBase, nonce string) string {
	q := [][2]string{{"v", "2"}, {"r", routerID}, {"n", routerName}, {"lan", lan}, {"port", strconv.Itoa(port)}}
	if publicBase != "" {
		q = append(q, [2]string{"pub", publicBase})
	}
	q = append(q, [2]string{"p", nonce})
	parts := make([]string, len(q))
	for i, kv := range q {
		parts[i] = kv[0] + "=" + escapeComponent(kv[1])
	}
	return "openbox://import?" + strings.Join(parts, "&")
}

// escapeComponent 与 JavaScript 的 encodeURIComponent 相同:字母数字和 - _ . ! ~ * ' ( ) 不转义,其余按 UTF-8 字节写 %XX(大写)。
func escapeComponent(s string) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexd[c>>4])
			b.WriteByte(hexd[c&15])
		}
	}
	return b.String()
}

// checkLan:空,或 IP,或主机名(字母数字、点、连字符、下划线)。进导入码前先收紧,不收奇怪的东西。
func checkLan(lan string) error {
	if lan == "" {
		return nil
	}
	if len(lan) > maxLanLen {
		return &InputError{"lan 太长"}
	}
	if net.ParseIP(lan) != nil {
		return nil
	}
	for _, c := range lan {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return &InputError{"lan 必须是 IP 地址或主机名"}
		}
	}
	return nil
}

// checkPublicBase:空,或 http(s) 地址,有主机名,不带账号密码。
func checkPublicBase(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > maxPublicBaseLen || !utf8.ValidString(p) {
		return &InputError{"publicBase 太长或不是合法文本"}
	}
	u, err := url.Parse(p)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return &InputError{"publicBase 必须是 http 或 https 地址,且不带账号密码"}
	}
	return nil
}
