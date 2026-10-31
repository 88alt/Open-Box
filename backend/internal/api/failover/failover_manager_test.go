package failover

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/failover"
	"github.com/timeflysoon/Open-Box/backend/internal/singbox"
)

type fakeMgr struct {
	mu       sync.Mutex
	running  bool
	runtime  []failover.GroupRuntime
	refresh  int
	rechecks []string
	known    map[string]bool
}

func (f *fakeMgr) Running() bool                     { return f.running }
func (f *fakeMgr) Snapshot() []failover.GroupRuntime { return f.runtime }
func (f *fakeMgr) Probes() (int, int)                { return 2, 4 }
func (f *fakeMgr) Refresh()                          { f.mu.Lock(); f.refresh++; f.mu.Unlock() }
func (f *fakeMgr) Recheck(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rechecks = append(f.rechecks, id)
	return f.known[id]
}

func withMgr(m *fakeMgr) Deps {
	return Deps{
		Manager: m,
		Plan:    func() (failover.Plan, error) { return planOne(), nil },
		Kernel: func(context.Context) ([]singbox.KernelProxy, error) {
			return []singbox.KernelProxy{{Name: "故障转移", Type: "Selector", Now: "HK-01"}}, nil
		},
	}
}

func TestManagerRefreshAndRecheck(t *testing.T) {
	m := &fakeMgr{known: map[string]bool{"g1": true}}
	d := withMgr(m)

	rec := do(t, d, http.MethodPost, "/api/openbox/failover/refresh", "")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` || m.refresh != 1 {
		t.Fatalf("refresh: %d %s refresh=%d", rec.Code, rec.Body, m.refresh)
	}
	rec = do(t, d, http.MethodPost, "/api/openbox/failover/recheck", `{"id":"g1"}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("recheck: %d %s", rec.Code, rec.Body)
	}
	// 管理器不认识的组:404,不是 501
	rec = do(t, d, http.MethodPost, "/api/openbox/failover/recheck", `{"id":"nope"}`)
	if rec.Code != http.StatusNotFound || strings.Join(m.rechecks, ",") != "g1,nope" {
		t.Fatalf("%d %v", rec.Code, m.rechecks)
	}
	if rec := do(t, d, http.MethodPost, "/api/openbox/failover/recheck", `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏请求体回 400: %d", rec.Code)
	}
	if len(m.rechecks) != 2 {
		t.Fatalf("坏请求体不该去叫管理器: %v", m.rechecks)
	}
}

func TestManagerStatusCarriesRuntime(t *testing.T) {
	at := int64(1700000000000)
	m := &fakeMgr{running: true, runtime: []failover.GroupRuntime{{
		ID: "g1", Status: "failing", LastError: "kernel", InFlight: true, RecheckPending: true, LastRoundAt: &at,
		Lanes: map[string]failover.LaneRuntime{"l1": {Health: "down", FailStreak: 2}},
	}}}
	rec := do(t, withMgr(m), http.MethodGet, "/api/openbox/failover/status", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body)
	}
	var st struct {
		Running bool
		Probes  struct{ Background, Limit int }
		Groups  []struct {
			Status         string
			InFlight       bool
			RecheckPending bool
			Lanes          []struct {
				Health     string
				FailStreak int
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.Probes.Background != 2 || st.Probes.Limit != 4 {
		t.Fatalf("%+v", st)
	}
	g := st.Groups[0]
	if g.Status != "failing" || !g.InFlight || !g.RecheckPending || g.Lanes[0].Health != "down" || g.Lanes[0].FailStreak != 2 {
		t.Fatalf("%+v", g)
	}
}

func TestNoManagerStillHonest(t *testing.T) {
	d := Deps{Plan: func() (failover.Plan, error) { return planOne(), nil }}
	if rec := do(t, d, http.MethodPost, "/api/openbox/failover/refresh", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("没有管理器时如实回 501: %d", rec.Code)
	}
}
