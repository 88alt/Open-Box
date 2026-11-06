package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/timeflysoon/Open-Box/backend/internal/contentfilter"
)

func fakeSnapshot() contentfilter.Snapshot {
	return contentfilter.Snapshot{
		SHA256: "abc123", Size: 77, Version: 1,
		Hostnames: []string{}, Rules: []contentfilter.Rule{}, Scripts: map[string]string{},
		Sets: []contentfilter.SetMeta{
			{ID: "cf1", Name: "去广告", Dialect: "surge", RuleCount: 2, ScriptCount: 1},
			{ID: "cf2", Name: "重写", Dialect: "quantumultx", RuleCount: 1},
		},
	}
}

func openPlain(t *testing.T, env Envelope, is Issue) map[string]json.RawMessage {
	t.Helper()
	plain, err := OpenEnvelope(env, is.PairCode, testIter, testIter)
	if err != nil {
		t.Fatalf("配对码应能解开: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(plain, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExportCarriesContentFilterSnapshot(t *testing.T) {
	s := fileSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) { return fakeSnapshot(), nil }
	ctx := context.Background()
	b, err := s.ExportFile(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(ctx)
	plain, err := OpenEnvelope(b.Seal(), is.PairCode, testIter, testIter)
	if err != nil {
		t.Fatal(err)
	}
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatal(err)
	}
	if p.V != 1 || p.Kind != "client-config" || len(p.ShareRegions) != 3 {
		t.Fatalf("原有内容不该变: %+v", p)
	}
	cf := p.ContentFilter
	if cf == nil || cf.SHA256 != "abc123" || cf.Size != 77 || len(cf.Sets) != 2 {
		t.Fatalf("快照: %+v", cf)
	}
	if got := cf.Sets[0]; got.ID != "cf1" || got.Name != "去广告" || got.Dialect != "surge" || got.RuleCount != 2 || got.ScriptCount != 1 {
		t.Fatalf("第一份: %+v", got)
	}
}

func TestSyncCarriesSameContentFilterSnapshot(t *testing.T) {
	s, is, _ := syncSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) { return fakeSnapshot(), nil }
	res, err := s.Sync(context.Background(), is.Nonce, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	m := openPlain(t, res.Seal(), is)
	var cf contentfilter.Snapshot
	if err := json.Unmarshal(m["contentFilter"], &cf); err != nil || cf.SHA256 != "abc123" || len(cf.Sets) != 2 || cf.Sets[1].ID != "cf2" {
		t.Fatalf("%v %+v", err, cf)
	}
}

func TestPayloadEmptySnapshotIsEmptyArrayNotNull(t *testing.T) {
	s := fileSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) {
		return contentfilter.Snapshot{SHA256: "e3b0", Size: 0, Sets: []contentfilter.SetMeta{}, Scripts: map[string]string{}}, nil
	}
	b, err := s.ExportFile(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	is, _ := s.ClientConfig(context.Background())
	var raw struct {
		Sets json.RawMessage `json:"sets"`
	}
	if err := json.Unmarshal(openPlain(t, b.Seal(), is)["contentFilter"], &raw); err != nil || string(raw.Sets) != "[]" {
		t.Fatalf("%s %v", raw.Sets, err)
	}
}

func TestContentFilterTooLargePropagates(t *testing.T) {
	s := fileSvc(t)
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) {
		return contentfilter.Snapshot{}, &contentfilter.TooLargeError{Size: 5 << 20, Max: 2 << 20}
	}
	_, err := s.ExportFile(context.Background(), "", "")
	var tl *ContentFilterTooLargeError
	if !errors.As(err, &tl) {
		t.Fatalf("%v", err)
	}
}

func TestContentFilterErrorPropagates(t *testing.T) {
	s := fileSvc(t)
	boom := errors.New("boom")
	s.ContentFilter = func(context.Context) (contentfilter.Snapshot, error) { return contentfilter.Snapshot{}, boom }
	if _, err := s.ExportFile(context.Background(), "", ""); !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
}
