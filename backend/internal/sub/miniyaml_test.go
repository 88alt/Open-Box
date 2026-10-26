package sub

import (
	"reflect"
	"strings"
	"testing"
)

func TestYAMLBlockAndFlow(t *testing.T) {
	src := `# 注释
proxies:
  - name: "a # b"   # 行尾注释
    type: ss
    port: 0123
    opts: {mode: websocket, headers: {Host: x.com}, list: [a, 'b c', "d"]}
    empty:
    off: false
  - {name: it's, type: vmess, n: ~}
- 不会被当成 proxies 的内容
`
	// 同缩进的序列:先单独测 proxies 段
	v, err := parseYAML(strings.Split(src, "- 不会")[0])
	if err != nil {
		t.Fatal(err)
	}
	got := v.(map[string]any)["proxies"].([]any)
	want := []any{
		map[string]any{
			"name": "a # b", "type": "ss", "port": "0123",
			"opts":  map[string]any{"mode": "websocket", "headers": map[string]any{"Host": "x.com"}, "list": []any{"a", "b c", "d"}},
			"empty": nil, "off": false,
		},
		map[string]any{"name": "it's", "type": "vmess", "n": nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestYAMLSeqSameIndentAndNested(t *testing.T) {
	v, err := parseYAML("proxies:\n- name: a\n  alpn:\n    - h2\n    - http/1.1\n- name: b\n  ws-opts:\n    path: /x\n    headers:\n      Host: h\n")
	if err != nil {
		t.Fatal(err)
	}
	ps := v.(map[string]any)["proxies"].([]any)
	if len(ps) != 2 {
		t.Fatalf("%#v", ps)
	}
	a, b := ps[0].(map[string]any), ps[1].(map[string]any)
	if !reflect.DeepEqual(a["alpn"], []any{"h2", "http/1.1"}) || b["ws-opts"].(map[string]any)["headers"].(map[string]any)["Host"] != "h" {
		t.Fatalf("%#v %#v", a, b)
	}
}

func TestYAMLMultilineFlow(t *testing.T) {
	v, err := parseYAML("proxies:\n  - {name: a,\n     type: ss,\n     alpn: [h2,\n       h3]}\n  - {name: b}\n")
	if err != nil {
		t.Fatal(err)
	}
	ps := v.(map[string]any)["proxies"].([]any)
	if len(ps) != 2 || !reflect.DeepEqual(ps[0].(map[string]any)["alpn"], []any{"h2", "h3"}) {
		t.Fatalf("%#v", ps)
	}
}

func TestYAMLAnchorsAndMerge(t *testing.T) {
	src := "base: &b {type: ss, cipher: aes-128-gcm, port: 1}\nproxies:\n  - {<<: *b, name: x, port: 2}\n  - <<: *b\n    name: y\n"
	v, err := parseYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	ps := v.(map[string]any)["proxies"].([]any)
	x, y := ps[0].(map[string]any), ps[1].(map[string]any)
	if x["port"] != "2" || x["cipher"] != "aes-128-gcm" || y["port"] != "1" || y["name"] != "y" {
		t.Fatalf("%#v %#v", x, y)
	}
}

func TestYAMLQuoting(t *testing.T) {
	v, err := parseYAML("a: \"x\\ty\\u4e2d\"\nb: 'it''s'\n\"c d\": 1\n")
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["a"] != "x\ty中" || m["b"] != "it's" || m["c d"] != "1" {
		t.Fatalf("%#v", m)
	}
}

func TestYAMLErrors(t *testing.T) {
	for _, src := range []string{
		"a: |\n  text\n",
		"a:\n\tb: 1\n",
		"a: {b: 1\n",
		"a: *nope\n",
		"a: \"abc\n",
		"a: 1\n  b: 2\n",
		"a: [1, }]\n",
	} {
		if _, err := parseYAML(src); err == nil {
			t.Errorf("应当报错:%q", src)
		}
	}
}
