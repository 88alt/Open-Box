package singbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/dnsdecide"
)

// 部署落盘的热切换开关文件,要能被穿透 / route-test 用的规则集判断(dnsdecide.FileSets)正确读成「开 = 匹配一切、关 = 永不命中」,
// 翻面后下一次预测读到新状态。这里把两边接起来走一遍:落盘 → 按配置登记的规则集读 → 判断。
func TestFlipFilesReadBackByDecisionSets(t *testing.T) {
	dir := t.TempDir()
	pa := filepath.Join(dir, "obflip-aaa.json")
	pb := filepath.Join(dir, "obflip-fallback.json")
	cfg := cfgWith(local("obflip-aaa", "source", pa), local("obflip-fallback", "source", pb))
	ctx := context.Background()
	sets := &dnsdecide.FileSets{}
	match := func(tag, domain string) (bool, error) {
		return sets.Bind(dnsdecide.SetDefsFromConfig(cfg)).Match(ctx, tag, domain)
	}
	deploy := func(flips map[string]bool) {
		t.Helper()
		rep, err := Files{}.WriteLocalSetsFlip(ctx, cfg, nil, flips)
		if err != nil || !rep.OK() {
			t.Fatalf("落盘失败 err=%v rep=%+v", err, rep)
		}
	}

	deploy(map[string]bool{"obflip-aaa": true, "obflip-fallback": false})
	for _, c := range []struct {
		tag  string
		want bool
	}{{"obflip-aaa", true}, {"obflip-fallback", false}} {
		got, err := match(c.tag, "example.com")
		if err != nil || got != c.want {
			t.Fatalf("%s: got=%v err=%v want=%v", c.tag, got, err, c.want)
		}
	}

	// 翻面:aaa 关、fallback 开;同一个 FileSets(带缓存)要读到新内容
	deploy(map[string]bool{"obflip-aaa": false, "obflip-fallback": true})
	for _, c := range []struct {
		tag  string
		want bool
	}{{"obflip-aaa", false}, {"obflip-fallback", true}} {
		got, err := match(c.tag, "example.com")
		if err != nil || got != c.want {
			t.Fatalf("翻面后 %s: got=%v err=%v want=%v", c.tag, got, err, c.want)
		}
	}

	// 回归:不能因为「开」的内容只有 network 条件就报判断不了
	if _, err := match("obflip-fallback", "any.test"); errors.Is(err, dnsdecide.ErrUnknownSet) {
		t.Fatalf("开着的开关不该是 ErrUnknownSet: %v", err)
	}
}
