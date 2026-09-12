package controlplane

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/goxang/lbplane/internal/haproxy"
)

type fakeHAProxy struct {
	checkErr error
	applyErr error
	reloads  int
	applied  [][]string
}

func (f *fakeHAProxy) Check(context.Context, string) error { return f.checkErr }

func (f *fakeHAProxy) Apply(_ context.Context, commands []string) error {
	f.applied = append(f.applied, commands)
	return f.applyErr
}

func (f *fakeHAProxy) Reload(context.Context) error {
	f.reloads++
	return nil
}

type harness struct {
	cp      *ControlPlane
	lb      *fakeHAProxy
	metrics *Metrics
	specDir string
	opts    Options
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	specDir := filepath.Join(dir, "services")
	if err := os.Mkdir(specDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		SpecDir:    specDir,
		ConfigPath: filepath.Join(dir, "haproxy.cfg"),
		HostsPath:  filepath.Join(dir, "hosts"),
		VIP:        "172.28.0.10",
		MinSlots:   4,
		Render:     haproxy.RenderOptions{AdminSocket: "/run/lb/admin.sock", HTTPPort: 80, HTTPSPort: 443},
	}
	lb := &fakeHAProxy{}
	metrics := NewMetrics(prometheus.NewRegistry())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &harness{cp: New(opts, lb, metrics, logger), lb: lb, metrics: metrics, specDir: specDir, opts: opts}
}

func (h *harness) writeCheckout(t *testing.T, backends ...string) {
	t.Helper()
	spec := "name: checkout\nowner: payments-team\nhostnames: [checkout.example.com]\nbackends:\n"
	for _, backend := range backends {
		spec += "  - address: " + backend + "\n"
	}
	if err := os.WriteFile(filepath.Join(h.specDir, "checkout.yaml"), []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) reconcile(t *testing.T) {
	t.Helper()
	if err := h.cp.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestBackendChangeIsAppliedWithoutReload(t *testing.T) {
	h := newHarness(t)
	h.writeCheckout(t, "10.0.0.1:8080")
	h.reconcile(t)

	h.writeCheckout(t, "10.0.0.1:8080", "10.0.0.2:8080")
	h.reconcile(t)

	if h.lb.reloads != 1 {
		t.Fatalf("reloads = %d, want only the initial one", h.lb.reloads)
	}
	if len(h.lb.applied) != 1 {
		t.Fatalf("runtime applies = %d, want 1", len(h.lb.applied))
	}
	if !strings.Contains(readFile(t, h.opts.ConfigPath), "server s2 10.0.0.2:8080 weight 100 check") {
		t.Error("runtime change must also be written to disk so a restart keeps it")
	}
	if got := readFile(t, h.opts.HostsPath); got != "172.28.0.10 checkout.example.com\n" {
		t.Errorf("hosts file = %q", got)
	}
}

func TestRejectedConfigKeepsRunningOne(t *testing.T) {
	h := newHarness(t)
	h.writeCheckout(t, "10.0.0.1:8080")
	h.reconcile(t)
	running := readFile(t, h.opts.ConfigPath)

	h.lb.checkErr = errors.New("haproxy -c: parsing error")
	h.writeCheckout(t, "10.0.0.1:8080", "10.0.0.2:8080", "10.0.0.3:8080", "10.0.0.4:8080", "10.0.0.5:8080")

	if err := h.cp.Reconcile(context.Background()); err == nil {
		t.Fatal("expected the rejected config to fail reconcile")
	}
	if readFile(t, h.opts.ConfigPath) != running {
		t.Error("config on disk changed although validation failed")
	}
	if h.lb.reloads != 1 {
		t.Errorf("reloads = %d, a rejected config must never be reloaded", h.lb.reloads)
	}
	if got := testutil.ToFloat64(h.metrics.failures.WithLabelValues("validate")); got != 1 {
		t.Errorf("validate failures = %v, want 1", got)
	}
}

func TestFailedRuntimeUpdateFallsBackToReload(t *testing.T) {
	h := newHarness(t)
	h.writeCheckout(t, "10.0.0.1:8080")
	h.reconcile(t)

	h.lb.applyErr = errors.New("No such server.")
	h.writeCheckout(t, "10.0.0.2:8080")
	h.reconcile(t)

	if h.lb.reloads != 2 {
		t.Fatalf("reloads = %d, want a fallback reload after the runtime failure", h.lb.reloads)
	}
}

func TestInvalidSpecDoesNotTouchHAProxy(t *testing.T) {
	h := newHarness(t)
	h.writeCheckout(t, "10.0.0.1:8080")
	h.reconcile(t)

	h.writeCheckout(t, "api.internal:8080")

	if err := h.cp.Reconcile(context.Background()); err == nil {
		t.Fatal("expected invalid spec to fail")
	}
	if h.lb.reloads != 1 || len(h.lb.applied) != 0 {
		t.Errorf("haproxy was touched: reloads=%d applies=%d", h.lb.reloads, len(h.lb.applied))
	}
}
