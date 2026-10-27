package dnscache

import (
	"context"
	"errors"
	"testing"
)

func TestFlushLayersIndependent(t *testing.T) {
	var order []string
	l := Layers{
		Kernel:   func(context.Context) error { order = append(order, "k"); return errors.New("连不上") },
		Dnsmasq:  func(context.Context) bool { order = append(order, "d"); return true },
		Resolved: func(context.Context) bool { order = append(order, "r"); return false },
	}
	got := l.Flush(context.Background())
	if got != (Result{Dnsmasq: true}) || !got.Any() || len(order) != 3 {
		t.Fatalf("%+v %v", got, order)
	}
}

func TestFlushNilLayersAndNone(t *testing.T) {
	if r := (Layers{}).Flush(context.Background()); r.Any() {
		t.Fatalf("%+v", r)
	}
	r := Layers{Kernel: func(context.Context) error { return nil }}.Flush(context.Background())
	if r != (Result{Kernel: true}) {
		t.Fatalf("%+v", r)
	}
}

func TestFlushPassesDeadline(t *testing.T) {
	Layers{
		Kernel:   func(c context.Context) error { assertDeadline(t, c); return nil },
		Dnsmasq:  func(c context.Context) bool { assertDeadline(t, c); return true },
		Resolved: func(c context.Context) bool { assertDeadline(t, c); return true },
	}.Flush(context.Background())
}

func assertDeadline(t *testing.T, c context.Context) {
	t.Helper()
	if _, ok := c.Deadline(); !ok {
		t.Fatal("每一层都该带超时")
	}
}
