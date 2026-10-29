package diagnostics

import (
	"net/url"
	"regexp"
	"strings"
)

// 脱敏(自己的设计,没有参照上游)。诊断包是用户自己下载、自己检查后再发到 issue 里的,
// 所以宁可多删一点:凭据一律删,地址类只留够排查用的部分。
//
//   - 按键名:密码、令牌、UUID、密钥、短 ID、用户名、SNI 等,值整个换成 Redacted;
//     有些键要看上下文:`server` 只有和 `server_port` 同在一个对象里才是节点地址(DNS 规则里的 `server` 是服务器标签,
//     排查 DNS 要用,留着),`host` / `path` / `headers` / `service_name` 只在 `transport` 里才抹(规则集的 `path` 是文件路径,留着);
//   - 按值:各协议的分享链接整条换掉,http(s) 链接只留「协议://主机」(订阅地址的路径和参数里常带令牌),
//     PEM 证书 / 私钥整段换掉;
//   - 键名和值两道各自独立生效,漏了一道还有另一道。

// Redacted 是被抹掉的值的占位。
const Redacted = "[已脱敏]"

// 键名先去掉 _ - 和空格、转小写再比;精确匹配这些名字,或者以 password / secret / token / key 结尾。
var sensitiveExact = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "uuid": true, "secret": true, "token": true,
	"psk": true, "presharedkey": true, "shortid": true, "auth": true, "authstr": true, "authpassword": true,
	"obfs": true, "obfspassword": true, "username": true, "user": true, "users": true, "key": true,
	"privatekey": true, "publickey": true, "certificate": true, "cert": true, "servername": true,
	"alterid": true, "sid": true, "email": true,
	"login": true, "loginid": true, "cookie": true, "authorization": true, "link": true, "links": true,
	"uri": true, "sni": true, "dest": true, "serviceaccount": true, "accesskey": true,
}

var sensitiveSuffix = []string{"password", "secret", "token", "key", "uuid", "passphrase"}

func sensitiveKey(k string) bool {
	n := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(k))
	if sensitiveExact[n] {
		return true
	}
	for _, s := range sensitiveSuffix {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

var (
	shareLinkRe = regexp.MustCompile(`(?i)\b(vless|vmess|trojan|ss|ssr|hysteria2?|hy2|tuic|wireguard|socks5?|anytls|naive\+https|juicity|mieru)://[^\s"'<>]+`)
	urlRe       = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)
	pemRe       = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+-----.*?(-----END [A-Z ]+-----|$)`)
)

// ScrubString 抹掉字符串里的分享链接、PEM 块,并把 http(s) 链接缩成「协议://主机」(去掉账号、路径、参数)。
func ScrubString(s string) string {
	s = pemRe.ReplaceAllString(s, "[PEM 已脱敏]")
	s = shareLinkRe.ReplaceAllString(s, "[分享链接已脱敏]")
	return urlRe.ReplaceAllStringFunc(s, func(m string) string {
		// 链接末尾常粘着标点
		trimmed := strings.TrimRight(m, ".,;:)]}")
		tail := m[len(trimmed):]
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return "[链接已脱敏]" + tail
		}
		out := u.Scheme + "://" + u.Hostname()
		if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
			out += "/…"
		}
		return out + tail
	})
}

// Redact 返回 v 脱敏后的副本(v 是 JSON 解码出来的 map / 数组 / 标量);不改动传入的值。
func Redact(v any) any { return redact(v, "") }

// transportKeys 是只在 transport 里才算敏感的键。
var transportKeys = map[string]bool{"host": true, "path": true, "headers": true, "servicename": true}

func redact(v any, parent string) any {
	switch x := v.(type) {
	case map[string]any:
		_, isNode := x["server_port"]
		out := make(map[string]any, len(x))
		for k, val := range x {
			n := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(k))
			switch {
			case sensitiveKey(k),
				n == "server" && isNode,
				parent == "transport" && transportKeys[n]:
				out[k] = redactValue(val)
			default:
				out[k] = redact(val, k)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redact(val, parent)
		}
		return out
	case string:
		return ScrubString(x)
	default:
		return v
	}
}

// redactValue 抹掉敏感键下的整个值:字符串换占位,数组 / 对象逐项换占位,数字和布尔也换(长度、取值本身可能是线索)。
// 空值保持空(「没填」本身对排查有用)。
func redactValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if x == "" {
			return ""
		}
		return Redacted
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = redactValue(e)
		}
		return out
	case bool:
		return x // 开关类布尔不是凭据,留着对排查有用
	default:
		return Redacted
	}
}
