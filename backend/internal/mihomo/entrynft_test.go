package mihomo

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// 对拍的向量是把上游 v0.1.462 Node(bfd33a70 的 panel/server/index.mjs)里的 ol / b0 / sl / w0 / mu
// 原样抽出来、在 Node 22 里跑出来的结果(见 testdata/entrynft_upstream.json):
//   - normRoutes 是规整后的终端分流(nr 的返回形状),给 ol / sl 用;
//   - rawRoutes 是原始档案行,给 w0(admit / bypass)与 b0 用;
//   - nft 是 mu 的若干组入参和整份 nft 片段。
type entryNftVectors struct {
	NormRoutes json.RawMessage `json:"normRoutes"`
	RawRoutes  json.RawMessage `json:"rawRoutes"`
	RouteCases []struct {
		Name       string          `json:"name"`
		Opts       json.RawMessage `json:"opts"`
		WantOl     []bool          `json:"wantOl"`
		WantSl     TerminalSet     `json:"wantSl"`
		WantB0     bool            `json:"wantB0"`
		WantBypass TerminalSet     `json:"wantBypass"`
		WantAdmit  TerminalSet     `json:"wantAdmit"`
	} `json:"routeCases"`
	Nft []struct {
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
		Want  string          `json:"want"`
	} `json:"nft"`
}

func loadEntryNftVectors(t *testing.T) entryNftVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/entrynft_upstream.json")
	if err != nil {
		t.Fatal(err)
	}
	var v entryNftVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// normTerminals 把 nil 与空切片统一成空切片,便于和向量比。
func normTerminals(s TerminalSet) TerminalSet {
	empty := func(x []string) []string {
		if len(x) == 0 {
			return []string{}
		}
		return append([]string{}, x...)
	}
	return TerminalSet{
		IPs: empty(s.IPs), Macs: empty(s.Macs), LocalIPs: empty(s.LocalIPs),
		LocalMacs: empty(s.LocalMacs), IPv6Macs: empty(s.IPv6Macs),
	}
}

func TestEntryRouteActiveUpstream(t *testing.T) {
	v := loadEntryNftVectors(t)
	var routes []ClientRoute
	if err := json.Unmarshal(v.NormRoutes, &routes); err != nil {
		t.Fatal(err)
	}
	var rawRoutes []any
	if err := json.Unmarshal(v.RawRoutes, &rawRoutes); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.RouteCases {
		var opts RouteOpts
		if err := json.Unmarshal(c.Opts, &opts); err != nil {
			t.Fatalf("%s: 选项读不出来: %v", c.Name, err)
		}
		if len(c.WantOl) != len(routes) {
			t.Fatalf("%s: 向量条数不对(%d vs %d)", c.Name, len(c.WantOl), len(routes))
		}
		var gotOl []bool
		for _, r := range routes {
			gotOl = append(gotOl, EntryRouteActive(r, opts))
		}
		if !reflect.DeepEqual(gotOl, c.WantOl) {
			t.Errorf("%s ol\n got: %v\nwant: %v", c.Name, gotOl, c.WantOl)
		}
		if got := normTerminals(EntryLists(routes, opts)); !reflect.DeepEqual(got, normTerminals(c.WantSl)) {
			t.Errorf("%s sl\n got: %s\nwant: %s", c.Name, encodeNoHTML(got), encodeNoHTML(c.WantSl))
		}
		if got := AdmitDNSEnabled(rawRoutes, opts); got != c.WantB0 {
			t.Errorf("%s b0: got %v want %v", c.Name, got, c.WantB0)
		}
		if got := normTerminals(BypassLists(rawRoutes)); !reflect.DeepEqual(got, normTerminals(c.WantBypass)) {
			t.Errorf("%s bypass\n got: %s\nwant: %s", c.Name, encodeNoHTML(got), encodeNoHTML(c.WantBypass))
		}
		if got := normTerminals(AdmitLists(rawRoutes)); !reflect.DeepEqual(got, normTerminals(c.WantAdmit)) {
			t.Errorf("%s admit\n got: %s\nwant: %s", c.Name, encodeNoHTML(got), encodeNoHTML(c.WantAdmit))
		}
	}
}

func TestBuildEntryNftUpstream(t *testing.T) {
	v := loadEntryNftVectors(t)
	for _, c := range v.Nft {
		var in EntryNftInput
		if err := json.Unmarshal(c.Input, &in); err != nil {
			t.Fatalf("%s: 入参读不出来: %v", c.Name, err)
		}
		if got := BuildEntryNft(in); got != c.Want {
			t.Errorf("%s\n got:\n%s\nwant:\n%s", c.Name, got, c.Want)
		}
	}
}

// 什么都没配时一个字节都不生成。
func TestBuildEntryNftEmpty(t *testing.T) {
	if got := BuildEntryNft(EntryNftInput{}); got != "" {
		t.Fatalf("应为空: %q", got)
	}
}

// 端口白名单要局域网口才写;端口串写坏当作没配。
func TestBuildEntryNftPorts(t *testing.T) {
	base := EntryNftInput{AutoRedirect: true}
	whitelist := base
	whitelist.Ports, whitelist.PortsMode = "500-510", "whitelist"
	if got := BuildEntryNft(whitelist); got != "" {
		t.Errorf("没有局域网口不该写白名单规则: %q", got)
	}
	whitelist.LANIfaces = []string{"br-lan"}
	if got := BuildEntryNft(whitelist); got == "" {
		t.Error("有局域网口应写白名单规则")
	}
	broken := base
	broken.Ports = "70000"
	if got := BuildEntryNft(broken); got != "" {
		t.Errorf("端口写坏应当作没配: %q", got)
	}
}
