package latency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 真实 Net.Coffee 答话的回放样本(2026-11-27 从设备上抓的,原样保存):
//
//	trace-ip.net.coffee.txt  —— GET https://ip.net.coffee/cdn-cgi/trace 的正文
//	netcoffee-lookup.json    —— GET https://ip.net.coffee/api/ipv2/lookup/<上面那个 ip>
//
// 为什么要这两份:链式测速的出口 IP 只查 Net.Coffee 一家(对齐上游 v0.1.396),
// 面板按 trace 取 ip、按 lookup 取国家 / 运营商。上游是闭源的,字段名只能靠真实答话核对,
// 存成样本后不用联网也能持续防回归。
const (
	traceFile  = "trace-ip.net.coffee.txt"
	lookupFile = "netcoffee-lookup.json"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读不到回放样本 %s: %v", name, err)
	}
	return b
}

// trace 必须是 ip.net.coffee 自己答的(h= 那行),ip 行要是公网 IPv4。
// 面板的 parseTraceIP 就靠这两个判据,任何一条不成立它都会抛错。
func TestNetCoffeeTraceFixture(t *testing.T) {
	text := string(readFixture(t, traceFile))
	kv := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimRight(line, "\r")
		if i := strings.Index(line, "="); i > 0 {
			kv[line[:i]] = strings.TrimSpace(line[i+1:])
		}
	}
	if kv["h"] != "ip.net.coffee" {
		t.Fatalf("h 必须是 ip.net.coffee,拿到 %q", kv["h"])
	}
	if kv["ip"] == "" {
		t.Fatal("trace 里没有 ip 行")
	}
	// 面板的 isIPLike 只认点分四段或含冒号的地址
	if !strings.Contains(kv["ip"], ".") && !strings.Contains(kv["ip"], ":") {
		t.Fatalf("ip 看起来不是地址: %q", kv["ip"])
	}
	if kv["loc"] == "" {
		t.Log("这份 trace 没有 loc(国家),面板只拿它当备用,不影响")
	}
	t.Logf("trace 样本:ip=%s loc=%s", kv["ip"], kv["loc"])
}

// lookup 里 parseLookup 要读的字段必须都在、且类型能直接用:
// ip 是字符串、country / countryCode / region / city 是字符串、asn 是数字(要转成字符串)、
// asOrganization / isp / company_name 至少有一个、is_bogon 为 false。
// 少任何一个,面板的出口 IP 那一行就会缺国家或运营商。
func TestNetCoffeeLookupFixture(t *testing.T) {
	var d map[string]any
	if err := json.Unmarshal(readFixture(t, lookupFile), &d); err != nil {
		t.Fatalf("lookup 样本不是合法 JSON: %v", err)
	}

	ip, _ := d["ip"].(string)
	if ip == "" {
		t.Fatal("lookup 里没有 ip")
	}
	// trace 与 lookup 必须是同一个 IP(面板会核对这件事,不匹配就报错)
	traceText := string(readFixture(t, traceFile))
	if !strings.Contains(traceText, "ip="+ip) {
		t.Fatalf("trace 的 ip 与 lookup 的 %s 不一致", ip)
	}

	for _, k := range []string{"country", "countryCode", "region", "city"} {
		v, ok := d[k].(string)
		if !ok || v == "" {
			t.Errorf("%s 应为非空字符串,拿到 %#v", k, d[k])
		}
	}

	// asn 是数字(实测),parseLookup 会 String() 之后去掉 AS 前缀
	switch v := d["asn"].(type) {
	case float64:
		if v <= 0 {
			t.Errorf("asn 应为正数,拿到 %v", v)
		}
	case string:
		if v == "" {
			t.Error("asn 是空字符串")
		}
	default:
		t.Errorf("asn 类型意外: %T", d["asn"])
	}

	// 三个运营商字段至少有一个非空(parseLookup 按这个顺序取:asOrganization → isp → company_name)
	org := ""
	for _, k := range []string{"asOrganization", "isp", "company_name"} {
		if v, _ := d[k].(string); v != "" {
			org = v
			break
		}
	}
	if org == "" {
		t.Error("asOrganization / isp / company_name 都是空的,面板显示不出运营商")
	}

	if bogon, ok := d["is_bogon"].(bool); ok && bogon {
		t.Error("样本 IP 被标成 bogon,面板会拒绝它")
	}
	if _, hasErr := d["error"]; hasErr {
		t.Error("样本里带 error,面板会拒绝它")
	}

	t.Logf("lookup 样本:ip=%s country=%s asn=%v org=%s", ip, d["country"], d["asn"], org)
}
