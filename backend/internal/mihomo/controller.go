package mihomo

import (
	"errors"
	"os"
	"regexp"
	"strings"
)

// 从 mihomo.yaml 里读控制接口(external-controller)和密钥(secret),给面板的 /api/controller* 转发用。
//
// 为什么不用完整的 YAML 解析:这里只要两个**顶层标量**,而且两种产出形态都要认——
//   - 自研装配用 MarshalYAML 写的是缩进式 YAML(`external-controller: "127.0.0.1:9095"`);
//   - 上游面板写的是单行 JSON(JSON 本身是合法 YAML,`"external-controller":"127.0.0.1:9095"`)。
// 两边的嵌套段里也有同名片段(例如 proxies 里的字段),所以只认行首(或 JSON 里的 `{`、`,` 之后)出现的顶层键,
// 且值必须是一个不带空格的标量,宁可读不出来报错,也不要猜一个地址去连。

var (
	// YAML:行首(允许缩进 0)的 key: value;JSON:紧跟 "{" 或 "," 的 "key":"value"。
	yamlControllerRe = regexp.MustCompile(`(?m)^[ \t]{0,2}external-controller:[ \t]*("[^"]*"|'[^']*'|[^\s#]+)`)
	yamlSecretRe     = regexp.MustCompile(`(?m)^[ \t]{0,2}secret:[ \t]*("[^"]*"|'[^']*'|[^\s#]+)`)
	jsonControllerRe = regexp.MustCompile(`[{,]"[ \t]*external-controller[ \t]*"[ \t]*:[ \t]*"([^"]*)"`)
	jsonSecretRe     = regexp.MustCompile(`[{,]"[ \t]*secret[ \t]*"[ \t]*:[ \t]*"([^"]*)"`)
)

// ControllerFromFile 读 <etc>/mihomo.yaml 里的控制接口地址与密钥。地址是空的返回 error(没开控制接口)。
func ControllerFromFile(path string) (addr, secret string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", errors.New("读不到 mihomo 配置(内核还没部署过?)")
	}
	text := string(b)
	addr = matchValue(jsonControllerRe, yamlControllerRe, text)
	if addr == "" {
		return "", "", errors.New("mihomo 配置里没有 external-controller")
	}
	return addr, matchValue(jsonSecretRe, yamlSecretRe, text), nil
}

// matchValue 先按 JSON 找,找不到再按 YAML 找,去掉引号。
func matchValue(jsonRe, yamlRe *regexp.Regexp, text string) string {
	if m := jsonRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	if m := yamlRe.FindStringSubmatch(text); m != nil {
		return unquote(strings.TrimSpace(m[1]))
	}
	return ""
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
