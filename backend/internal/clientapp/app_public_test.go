package clientapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHomeAndRegions(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	is, err := s.ClientConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Home(ctx, is.RouterID); err != nil {
		t.Fatal(err)
	}
	if err := s.Home(ctx, "deadbeefdeadbeefdeadbeefdeadbeef"); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("%v", err)
	}
	p, err := s.Regions(ctx, is.RouterID)
	if err != nil || len(p.Groups) != 3 || p.Version == "" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestPublicGeoAndBackground(t *testing.T) {
	s := fileSvc(t)
	ctx := context.Background()
	is, _ := s.ClientConfig(ctx)
	dir := t.TempDir()
	s.GeoDir = dir
	path := filepath.Join(dir, "geosite-cn.srs")
	if err := os.WriteFile(path, []byte("srs"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.PublicGeoPath(ctx, is.RouterID, "geosite-cn")
	if err != nil || got != path {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := s.PublicBackground(ctx, is.RouterID); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("no bg: %v", err)
	}
}
