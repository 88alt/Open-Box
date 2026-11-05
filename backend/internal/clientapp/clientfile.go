package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/timeflysoon/Open-Box/backend/internal/contentfilter"
)

// POST /api/openbox/client-config/file:导出文件。把本地分流的配置用配对码封好,连同「导入码」一起给面板存成 .json。
// 对齐上游 v0.1.402 封包外形(open-box-client-sealed + PBKDF2 20 万次 + AES-GCM);导入码仍按前端 helper/clientAppCode.ts 字段顺序拼进二维码。
//
// 导出不看同步开关:开关管的是 App 用的公开接口,文件是用户手动拿走的。
// 导出文件等于凭据(配对码 4 位,文件被拿走后可离线爆破,20 万次 PBKDF2 只是拖慢),面板的响应是 no-store。

const (
	// BundleFormat 对齐上游 open-box-client-sealed。
	BundleFormat = "open-box-client-sealed"
	// BundleVersion 对齐上游 version:1。
	BundleVersion = 1

	maxLanLen        = 253
	maxPublicBaseLen = 2048
)

// BundleRouter 是导出文件里的路由器标识。
type BundleRouter struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Bundle 是导出文件的内容,外形对齐上游 v0.1.402:
// format / version / router / kdf / data;data = base64url(iv||ct||tag)。
// 前端当 Record<string, unknown> 原样存成 JSON。
type Bundle struct {
	Format     string       `json:"format"`
	Version    int          `json:"version"`
	Router     BundleRouter `json:"router"`
	KDF        KDFParams    `json:"kdf"`
	Data       string       `json:"data"`
	ExportedAt int64        `json:"exportedAt,omitempty"`
}

// Seal 方便测试与公开同步接口取同一份密文。
func (b Bundle) Seal() Envelope { return Envelope{KDF: b.KDF, Data: b.Data} }

// payload 是封进去的明文:App 导入后要用的整份本地分流配置。
// shareRegions 是此刻生效的地区分流(没改过 = 默认三组)。App 发给内核时每组最前面要自己带上
// 「lanSubnets 这些网段 → 回路由器」(前端对 lanSubnets 的说明),所以这里只给网段、不改每组的规则。
//
// contentFilter 是启用的内容过滤集合的快照(规则和脚本的文本,App 在手机上执行,见 contentfilter.Snapshot):
// {sha256, size, sets:[{id, name, dialect, ruleCount, scriptCount, updatedAt, text}]},sha256 / size 与 GET /content-filter
// 同一口径,App 拿它判断本地的快照要不要换。这一项是新增的可选键,payload 的 v 仍是 1(旧 App 忽略不认识的键);
// 没有接内容过滤服务时整个键不出现,接了而没有启用的集合时是空的 sets。
// 不分块:启用集合总量有 4 MiB 的上限(contentfilter.MaxSnapshotSize),整份放进同一个封包,局域网里一次传完。
type payload struct {
	V             int                     `json:"v"`
	Kind          string                  `json:"kind"`
	GeneratedAt   int64                   `json:"generatedAt"`
	Router        payloadRtr              `json:"router"`
	ShareRegions  []Group                 `json:"shareRegions"`
	ContentFilter *contentfilter.Snapshot `json:"contentFilter,omitempty"`
	ImportCode    string                  `json:"importCode,omitempty"`
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

// ContentFilterTooLargeError:启用的内容过滤规则总量超过同步上限,导出和同步都不给(不截断、不挑几份)。
type ContentFilterTooLargeError struct{ Size, Max int }

func (e *ContentFilterTooLargeError) Error() string {
	return (&contentfilter.TooLargeError{Size: e.Size, Max: e.Max}).Error() + ",请先在「内容过滤」里停用或删除一些"
}

// maxConcurrentSeals 是同一时刻最多几份封包在生成。明文最大约 4 MiB,生成时内存里同时有明文、密文、base64 和响应体,
// 路由器上不能让一堆同步请求各占一份;多出来的请求等着(可随连接断开取消),不拒绝。
const maxConcurrentSeals = 2

func (s *Service) acquireSeal(ctx context.Context) (release func(), err error) {
	s.sealOnce.Do(func() { s.sealSem = make(chan struct{}, maxConcurrentSeals) })
	select {
	case s.sealSem <- struct{}{}:
		return func() { <-s.sealSem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

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
	code := ImportCode(is.RouterID, is.RouterName, lan, is.PanelPort, publicBase, is.Nonce)
	env, now, err := s.sealConfig(ctx, is, code)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		Format: BundleFormat, Version: BundleVersion,
		Router: BundleRouter{ID: is.RouterID, Name: is.RouterName},
		KDF:    env.KDF, Data: env.Data, ExportedAt: now,
	}, nil
}

// sealConfig 把此刻的本地分流配置按 is 里的配对码封好。导出文件和公开同步接口共用,返回封包和生成时刻(毫秒)。
// importCode 非空时写入明文(导出文件需要;公开同步可空)。
func (s *Service) plainConfig(ctx context.Context, is Issue, importCode string) ([]byte, int64, error) {
	info, err := s.Info(ctx)
	if err != nil {
		return nil, 0, err
	}
	var cf *contentfilter.Snapshot
	if s.ContentFilter != nil {
		sn, err := s.ContentFilter(ctx)
		if err != nil {
			var tl *contentfilter.TooLargeError
			if errors.As(err, &tl) {
				return nil, 0, &ContentFilterTooLargeError{Size: tl.Size, Max: tl.Max}
			}
			return nil, 0, fmt.Errorf("读取内容过滤失败: %w", err)
		}
		cf = &sn
	}
	now := s.now().UnixMilli()
	plain, err := json.Marshal(payload{
		V: 1, Kind: "client-config", GeneratedAt: now,
		Router: payloadRtr{
			ID: info.RouterID, Name: info.RouterName, LanAddresses: info.LanAddresses,
			LanSubnets: info.LanSubnets, PanelPort: info.PanelPort, ServerInfo: info.ServerInfo,
		},
		ShareRegions:  info.ShareRegions,
		ContentFilter: cf,
		ImportCode:    importCode,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("编码配置失败: %w", err)
	}
	return plain, now, nil
}

func (s *Service) sealConfig(ctx context.Context, is Issue, importCode string) (Envelope, int64, error) {
	release, err := s.acquireSeal(ctx)
	if err != nil {
		return Envelope{}, 0, err
	}
	defer release()
	plain, now, err := s.plainConfig(ctx, is, importCode)
	if err != nil {
		return Envelope{}, 0, err
	}
	iter := s.kdfIter()
	env, err := seal(is.PairCode, iter, plain, s.Rand)
	if err != nil {
		return Envelope{}, 0, fmt.Errorf("封包失败: %w", err)
	}
	if salt, err := b64.DecodeString(env.KDF.Salt); err == nil {
		_, _ = s.keys.get(is.PairCode, salt, iter)
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
