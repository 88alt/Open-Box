package singbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// 热切换开关文件(flip-flag)的落盘。内容与 core 的 FlipFlagOn / FlipFlagOff 逐字节一致(期望串直接写在这里,不引用被测常量)。
const (
	wantFlipOn  = `{"version":3,"rules":[{"network":["tcp","udp"]}]}`
	wantFlipOff = `{"version":3,"rules":[{"domain":["obflip-off.invalid"]}]}`
)

func TestWriteLocalSetsFlipFlags(t *testing.T) {
	dir := t.TempDir()
	pa, pb := filepath.Join(dir, "flip", "obflip-aaa.json"), filepath.Join(dir, "flip", "obflip-fallback.json")
	cfg := cfgWith(local("obflip-aaa", "source", pa), local("obflip-fallback", "source", pb))
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	// 开着的写「匹配一切」,关着的写「永不命中」;目录不存在会建
	rep, err := Files{}.WriteLocalSetsFlip(context.Background(), cfg, nil, map[string]bool{"obflip-aaa": true, "obflip-fallback": false})
	if err != nil || !rep.OK() || len(rep.Written) != 2 || len(rep.Kept) != 0 {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
	if read(pa) != wantFlipOn || read(pb) != wantFlipOff {
		t.Fatalf("内容 a=%s b=%s", read(pa), read(pb))
	}

	// 状态没变:原样保留,不改写
	rep, _ = Files{}.WriteLocalSetsFlip(context.Background(), cfg, nil, map[string]bool{"obflip-aaa": true, "obflip-fallback": false})
	if !rep.OK() || len(rep.Written) != 0 || len(rep.Kept) != 2 {
		t.Fatalf("没变时应保留: %+v", rep)
	}

	// 状态翻面:只改翻了的那个,不留临时文件
	rep, _ = Files{}.WriteLocalSetsFlip(context.Background(), cfg, nil, map[string]bool{"obflip-aaa": false, "obflip-fallback": false})
	if !rep.OK() || len(rep.Written) != 1 || rep.Written[0] != pa || len(rep.Kept) != 1 {
		t.Fatalf("翻面: %+v", rep)
	}
	if read(pa) != wantFlipOff {
		t.Fatalf("翻面后 a=%s", read(pa))
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "flip", ".*")); len(left) != 0 {
		t.Fatalf("残留临时文件: %v", left)
	}

	// 手动被改坏的文件按最新状态改回来
	if err := os.WriteFile(pb, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _ = Files{}.WriteLocalSetsFlip(context.Background(), cfg, nil, map[string]bool{"obflip-aaa": false, "obflip-fallback": true})
	if !rep.OK() || read(pb) != wantFlipOn {
		t.Fatalf("改坏的文件: %+v b=%s", rep, read(pb))
	}
}

func TestWriteLocalSetsFlipFlagsMissing(t *testing.T) {
	dir := t.TempDir()
	pa := filepath.Join(dir, "obflip-aaa.json")
	cfg := cfgWith(local("obflip-aaa", "source", pa))

	for name, flips := range map[string]map[string]bool{
		"core 没给(nil)": nil,
		"给了但没有这个 tag":  {"obflip-other": true},
	} {
		rep, err := Files{}.WriteLocalSetsFlip(context.Background(), cfg, nil, flips)
		if err != nil || rep.OK() || len(rep.Missing) != 1 || rep.Missing[0].Kind != KindFlipFlag || rep.Missing[0].Reason == "" {
			t.Fatalf("%s: err=%v rep=%+v", name, err, rep)
		}
		if _, err := os.Stat(pa); err == nil {
			t.Fatalf("%s: 不该写文件", name)
		}
	}

	// 老入口 WriteLocalSets 没有开关状态,行为不变:报缺
	rep, _ := Files{}.WriteLocalSets(context.Background(), cfg, nil)
	if rep.OK() || len(rep.Missing) != 1 || rep.Missing[0].Kind != KindFlipFlag {
		t.Fatalf("老入口: %+v", rep)
	}

	// 格式不对(binary)报缺,不写
	cfg = cfgWith(local("obflip-aaa", "binary", pa))
	rep, _ = Files{}.WriteLocalSetsFlip(context.Background(), cfg, nil, map[string]bool{"obflip-aaa": true})
	if rep.OK() || len(rep.Written) != 0 {
		t.Fatalf("格式不对: %+v", rep)
	}
}

// 真实 core 生成的配置:Built.FlipFlags 的键正好是配置里 flip-flag 规则集的 tag,落盘后每个文件的内容和状态一致。
// 只取 flip-flag 这一类落盘,不需要 sing-box。
func TestWriteLocalSetsFlipFlagsFromRealBuild(t *testing.T) {
	e := newEnv(t, System{})
	e.addSub(t, "A", twoNodes)
	built, err := e.gen.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(built.FlipFlags) == 0 {
		t.Fatalf("core 没给 flipFlags: %+v", built.FlipFlags)
	}
	root := t.TempDir()
	var sets []any
	inConfig := map[string]string{}
	for _, s := range LocalSets(built.Config) {
		if Classify(s) != KindFlipFlag {
			continue
		}
		p := filepath.Join(root, s.Tag+".json")
		inConfig[s.Tag] = p
		sets = append(sets, map[string]any{"type": "local", "tag": s.Tag, "format": s.Format, "path": p})
	}
	if len(inConfig) != len(built.FlipFlags) {
		t.Fatalf("配置里的开关 %v,flipFlags %v", inConfig, built.FlipFlags)
	}
	cfg := map[string]any{"route": map[string]any{"rule_set": sets}}
	rep, err := Files{}.WriteLocalSetsFlip(context.Background(), cfg, nil, built.FlipFlags)
	if err != nil || !rep.OK() || len(rep.Written) != len(inConfig) {
		t.Fatalf("err=%v rep=%+v", err, rep)
	}
	sawOn, sawOff := false, false
	for tag, on := range built.FlipFlags {
		p, ok := inConfig[tag]
		if !ok {
			t.Fatalf("flipFlags 里的 %s 不在配置里", tag)
		}
		b, _ := os.ReadFile(p)
		want := wantFlipOff
		if on {
			want = wantFlipOn
		}
		if string(b) != want {
			t.Errorf("%s(on=%v)内容 %s", tag, on, b)
		}
		sawOn, sawOff = sawOn || on, sawOff || !on
	}
	// 默认档案:国内直连的站点集走直连(关),兜底走节点组(开)——两种内容都真的落过盘
	if !sawOn || !sawOff {
		t.Errorf("默认档案应同时有开和关: %v", built.FlipFlags)
	}
}
