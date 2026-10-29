package deploystate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
	"github.com/timeflysoon/Open-Box/backend/internal/store"
)

func newSvc(t *testing.T, res singbox.DeployResult) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1_700_000_000_123)
	return &Service{Store: st, Deploy: func(context.Context) singbox.DeployResult { return res }, Now: func() time.Time { return at }}, dir
}

func TestStateIdleWhenNeverDeployed(t *testing.T) {
	s, _ := newSvc(t, singbox.DeployResult{})
	st, err := s.State()
	if err != nil || st.Stage != StageIdle || st.At != 0 || st.Message != "" || st.BadTags == nil {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestRunRecordsSuccessAndFailure(t *testing.T) {
	s, _ := newSvc(t, singbox.DeployResult{OK: true, Stage: singbox.StageRunning, Message: "已应用并正常运行", BadTags: []string{}})
	if res := s.Run(context.Background()); !res.OK {
		t.Fatalf("%+v", res)
	}
	st, _ := s.State()
	if st.Stage != "running" || st.Message != "已应用并正常运行" || st.At != 1_700_000_000_123 || len(st.BadTags) != 0 {
		t.Fatalf("%+v", st)
	}

	// 失败覆盖成功,带 badTags 和回滚说明
	s.Deploy = func(context.Context) singbox.DeployResult {
		return singbox.DeployResult{Stage: singbox.StageValidate, Message: "配置引用的规则集还没准备好,未应用", BadTags: []string{"geosite-x"}}
	}
	if res := s.Run(context.Background()); res.OK {
		t.Fatal("应该失败")
	}
	st, _ = s.State()
	if st.Stage != "validate" || len(st.BadTags) != 1 || st.BadTags[0] != "geosite-x" {
		t.Fatalf("%+v", st)
	}
	// 失败结果里 nil 的 BadTags 存成 []
	s.Deploy = func(context.Context) singbox.DeployResult {
		return singbox.DeployResult{Stage: singbox.StageStart, Message: "x"}
	}
	s.Run(context.Background())
	if st, _ = s.State(); st.BadTags == nil {
		t.Fatal("badTags 不能是 null")
	}
}

func TestBusyNotRecorded(t *testing.T) {
	s, _ := newSvc(t, singbox.DeployResult{OK: true, Stage: singbox.StageRunning})
	s.Run(context.Background())
	s.Deploy = func(context.Context) singbox.DeployResult {
		return singbox.DeployResult{Stage: singbox.StageError, Message: "已有一次部署正在进行,请稍后再试", Busy: true}
	}
	if res := s.Run(context.Background()); !res.Busy {
		t.Fatalf("%+v", res)
	}
	if st, _ := s.State(); st.Stage != "running" {
		t.Fatalf("什么都没做的结果不该盖掉上一次的记录: %+v", st)
	}
}

func TestLongMessageTruncated(t *testing.T) {
	s, _ := newSvc(t, singbox.DeployResult{Stage: singbox.StageValidate, Message: strings.Repeat("长", 5000)})
	s.Run(context.Background())
	st, _ := s.State()
	if n := len([]rune(st.Message)); n != maxMsgLen+1 || !strings.HasSuffix(st.Message, "…") {
		t.Fatalf("长度 %d", n)
	}
}

func TestPersistsAcrossInstancesAndCorruptFileIsAnError(t *testing.T) {
	s, dir := newSvc(t, singbox.DeployResult{OK: true, Stage: singbox.StageRunning, Message: "ok"})
	s.Run(context.Background())
	st2, _ := store.Open(dir)
	s2 := &Service{Store: st2}
	if got, err := s2.State(); err != nil || got.Stage != "running" || got.Message != "ok" {
		t.Fatalf("重新打开后应该还在: %+v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName+".json"), []byte("{坏了"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.State(); err == nil {
		t.Fatal("文件坏了应该报错而不是当成 idle")
	}
}
