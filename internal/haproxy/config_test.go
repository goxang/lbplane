package haproxy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/goxang/lbplane/internal/spec"
)

func service(name string, addresses ...string) spec.Service {
	svc := spec.Service{Name: name, Owner: "team", Hostnames: []string{name + ".example.com"}, Balance: "roundrobin"}
	for _, address := range addresses {
		svc.Backends = append(svc.Backends, spec.Backend{Address: address})
	}
	return svc
}

func TestBuildKeepsSlotPositionsSoBackendSwapNeedsNoReload(t *testing.T) {
	previous := Build([]spec.Service{service("checkout", "10.0.0.1:80", "10.0.0.2:80")}, Config{}, 4)
	next := Build([]spec.Service{service("checkout", "10.0.0.2:80", "10.0.0.3:80")}, previous, 4)

	if !SameShape(previous, next) {
		t.Fatal("swapping one backend must not change the config shape")
	}
	wantSlots := []Slot{{"10.0.0.3:80", 100}, {"10.0.0.2:80", 100}, {}, {}}
	if got := next.Services[0].Slots; !reflect.DeepEqual(got, wantSlots) {
		t.Fatalf("slots = %v, want %v", got, wantSlots)
	}
	wantCommands := []string{
		"set server be_checkout/s1 addr 10.0.0.3 port 80",
		"set server be_checkout/s1 weight 100",
		"set server be_checkout/s1 state ready",
	}
	if got := RuntimeCommands(previous, next); !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("commands = %q, want %q", got, wantCommands)
	}
}

func TestBuildNeedsReloadWhenBackendsOutgrowSlots(t *testing.T) {
	previous := Build([]spec.Service{service("checkout", "10.0.0.1:80")}, Config{}, 2)
	next := Build([]spec.Service{service("checkout", "10.0.0.1:80", "10.0.0.2:80", "10.0.0.3:80")}, previous, 2)

	if SameShape(previous, next) {
		t.Fatal("more backends than slots must force a reload")
	}
	if got := len(next.Services[0].Slots); got != 6 {
		t.Fatalf("slots = %d, want headroom of 6", got)
	}
}

func TestRenderRoutesHostsAndDisablesFreeSlots(t *testing.T) {
	cfg := Build([]spec.Service{service("checkout", "10.0.0.1:80")}, Config{}, 2)

	rendered, err := Render(cfg, RenderOptions{AdminSocket: "/run/lb/admin.sock", HTTPPort: 80, HTTPSPort: 443})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"use_backend be_checkout if { req.hdr(host),field(1,:),lower -m str checkout.example.com }",
		"server s1 10.0.0.1:80 weight 100 check",
		"server s2 127.0.0.1:1 weight 0 check disabled",
	} {
		if !strings.Contains(string(rendered), line) {
			t.Errorf("rendered config is missing %q\n%s", line, rendered)
		}
	}
	if strings.Contains(string(rendered), "ssl") {
		t.Error("no service has a certificate, so no TLS bind should be rendered")
	}
}

func TestRenderedConfigPassesHAProxyCheck(t *testing.T) {
	binary, err := exec.LookPath("haproxy")
	if err != nil {
		t.Skip("haproxy not installed")
	}
	cfg := Build([]spec.Service{service("checkout", "10.0.0.1:80"), service("refunds", "10.0.1.1:80")}, Config{}, 4)
	rendered, err := Render(cfg, RenderOptions{AdminSocket: filepath.Join(t.TempDir(), "admin.sock"), HTTPPort: 8080, HTTPSPort: 8443})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(path, rendered, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := (Client{Binary: binary}).Check(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}
