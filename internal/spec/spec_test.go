package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSpecs(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const checkoutSpec = `
name: checkout
owner: payments-team
hostnames: [Checkout.Example.com]
health_check: /healthz
backends:
  - address: 10.0.0.1:8080
  - address: 10.0.0.2:8080
    weight: 50
`

func TestLoadDirAppliesDefaults(t *testing.T) {
	dir := writeSpecs(t, map[string]string{"checkout.yaml": checkoutSpec})

	services, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("got %d services, want 1", len(services))
	}
	svc := services[0]
	if svc.Balance != "roundrobin" {
		t.Errorf("balance = %q, want roundrobin", svc.Balance)
	}
	if svc.Hostnames[0] != "checkout.example.com" {
		t.Errorf("hostname = %q, want lowercased", svc.Hostnames[0])
	}
	if w := svc.Backends[0].EffectiveWeight(); w != DefaultWeight {
		t.Errorf("default weight = %d, want %d", w, DefaultWeight)
	}
}

func TestLoadDirRejectsHostnameClaimedByAnotherTeam(t *testing.T) {
	dir := writeSpecs(t, map[string]string{
		"checkout.yaml": checkoutSpec,
		"refunds.yaml": `
name: refunds
owner: refunds-team
hostnames: [checkout.example.com]
backends: [{address: 10.0.1.1:8080}]
`,
	})

	_, err := LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), `hostname "checkout.example.com" already claimed by checkout.yaml`) {
		t.Fatalf("expected hostname conflict, got %v", err)
	}
}

func TestLoadDirFailsWholeSetOnOneBrokenFile(t *testing.T) {
	dir := writeSpecs(t, map[string]string{
		"checkout.yaml": checkoutSpec,
		"broken.yaml":   "name: broken\nownr: typo\n",
	})

	services, err := LoadDir(dir)
	if err == nil {
		t.Fatal("expected an error for the unknown field")
	}
	if services != nil {
		t.Errorf("expected no services on error, got %d", len(services))
	}
}

func TestValidateRejectsUnsafeBackends(t *testing.T) {
	weight := 300
	tests := map[string]struct {
		backend Backend
		want    string
	}{
		"hostname address": {Backend{Address: "api.internal:8080"}, "must be an IP address"},
		"missing port":     {Backend{Address: "10.0.0.1"}, "missing port"},
		"weight too high":  {Backend{Address: "10.0.0.1:8080", Weight: &weight}, "out of range"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			svc := Service{Name: "checkout", Owner: "payments-team", Hostnames: []string{"checkout.example.com"}, Balance: "roundrobin", Backends: []Backend{tc.backend}}
			err := svc.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
