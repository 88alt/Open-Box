package singbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const connsJSON = `{"downloadTotal":5000,"uploadTotal":1234,"memory":1,"connections":[
 {"id":"a","upload":10,"download":20,"chains":["节点A","其他"],"metadata":{"sourceIP":"192.168.1.5","host":"example.com","destinationIP":"1.2.3.4"}},
 {"id":"","upload":1,"download":1},
 {"id":"b","upload":0,"download":7,"chains":["直连","其他"],"metadata":{"sourceIP":"192.168.1.6","destinationIP":"9.9.9.9"}}]}`

func TestParseKernelConnections(t *testing.T) {
	s, err := parseKernelConnections(strings.NewReader(connsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if s.UploadTotal != 1234 || s.DownloadTotal != 5000 || len(s.Conns) != 2 {
		t.Fatalf("%+v", s)
	}
	want := KernelConn{ID: "a", Upload: 10, Download: 20, Chains: []string{"节点A", "其他"}, Source: "192.168.1.5", Host: "example.com", DestIP: "1.2.3.4"}
	if !reflect.DeepEqual(s.Conns[0], want) {
		t.Errorf("%+v", s.Conns[0])
	}
	if s.Conns[1].Host != "" || s.Conns[1].DestIP != "9.9.9.9" {
		t.Errorf("%+v", s.Conns[1])
	}
	if _, err := parseKernelConnections(strings.NewReader("不是 json")); err == nil {
		t.Error("坏 JSON 该报错")
	}
}

func TestKernelConnections(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(connsJSON))
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")
	write := func(s string) {
		if err := os.WriteFile(cfgFile, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"outbounds":[{"type":"direct","tag":"直连"},{"type":"selector","tag":"其他"},{"type":"direct","tag":"d2"}],
	 "experimental":{"clash_api":{"external_controller":"` + strings.TrimPrefix(srv.URL, "http://") + `","secret":"s3"}}}`)
	snap, err := KernelConnections(context.Background(), cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer s3" || gotPath != "/connections" || len(snap.Conns) != 2 || !reflect.DeepEqual(snap.DirectTags, []string{"直连", "d2"}) {
		t.Errorf("auth=%q path=%q snap=%+v", gotAuth, gotPath, snap)
	}

	// 没开控制接口、配置不存在、控制接口连不上
	write(`{"outbounds":[]}`)
	if _, err := KernelConnections(context.Background(), cfgFile); err == nil || !strings.Contains(err.Error(), "控制接口") {
		t.Errorf("没开控制接口: %v", err)
	}
	if _, err := KernelConnections(context.Background(), filepath.Join(dir, "无")); err == nil {
		t.Error("配置不存在该报错")
	}
	write(`{"experimental":{"clash_api":{"external_controller":"127.0.0.1:1"}}}`)
	if _, err := KernelConnections(context.Background(), cfgFile); err == nil || !strings.Contains(err.Error(), "连不上") {
		t.Errorf("连不上: %v", err)
	}
}

func TestParseKernelConnectionsSourcePort(t *testing.T) {
	body := `{"uploadTotal":0,"downloadTotal":0,"connections":[
	 {"id":"1","chains":["n","g"],"metadata":{"sourceIP":"127.0.0.1","sourcePort":"54321"}},
	 {"id":"2","chains":["n"],"metadata":{"sourceIP":"127.0.0.1","sourcePort":8080}},
	 {"id":"3","chains":["n"],"metadata":{"sourceIP":"127.0.0.1"}}]}`
	snap, err := parseKernelConnections(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"54321", "8080", ""}
	for i, c := range snap.Conns {
		if c.SourcePort != want[i] {
			t.Errorf("第 %d 条来源端口 %q,想要 %q", i+1, c.SourcePort, want[i])
		}
	}
}
