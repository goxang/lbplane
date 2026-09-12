package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/goxang/lbplane/internal/controlplane"
	"github.com/goxang/lbplane/internal/haproxy"
	"github.com/goxang/lbplane/internal/spec"
)

const usage = `usage:
  lbplane run      [flags]   reconcile service specs into a running HAProxy
  lbplane validate [flags]   check service specs, e.g. in a team's CI`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "validate":
		if err := validate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("lbplane failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("run", flag.ExitOnError)
	var (
		opts        controlplane.Options
		client      haproxy.Client
		interval    time.Duration
		metricsAddr string
	)
	flags.StringVar(&opts.SpecDir, "specs", "services", "directory of service spec files")
	flags.StringVar(&opts.ConfigPath, "haproxy-config", "/run/lb/haproxy.cfg", "generated HAProxy config path")
	flags.StringVar(&opts.HostsPath, "dns-hosts", "", "hosts file for CoreDNS; empty disables DNS publishing")
	flags.StringVar(&opts.VIP, "vip", "", "address the published hostnames resolve to")
	flags.IntVar(&opts.MinSlots, "min-slots", 4, "minimum server slots per backend, spare slots allow changes without reload")
	flags.IntVar(&opts.Render.HTTPPort, "http-port", 80, "HAProxy HTTP listen port")
	flags.IntVar(&opts.Render.HTTPSPort, "https-port", 443, "HAProxy HTTPS listen port")
	flags.StringVar(&opts.Render.AdminSocket, "admin-socket", "/run/lb/admin.sock", "HAProxy runtime API socket")
	flags.StringVar(&client.MasterSocket, "master-socket", "/run/lb/master.sock", "HAProxy master CLI socket")
	flags.StringVar(&client.Binary, "haproxy-bin", "haproxy", "HAProxy binary used to validate configs")
	flags.DurationVar(&interval, "interval", 5*time.Second, "how often specs are re-read")
	flags.StringVar(&metricsAddr, "metrics-addr", ":9100", "address for /metrics and /healthz")
	_ = flags.Parse(args)

	if opts.HostsPath != "" && opts.VIP == "" {
		return errors.New("--vip is required when --dns-hosts is set")
	}
	client.AdminSocket = opts.Render.AdminSocket

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	registry := prometheus.NewRegistry()
	cp := controlplane.New(opts, client, controlplane.NewMetrics(registry), logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	server := &http.Server{Addr: metricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server stopped", "error", err)
		}
	}()

	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.Info("lbplane started", "specs", opts.SpecDir, "interval", interval)
	for {
		if err := cp.Reconcile(ctx); err != nil {
			logger.Error("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return server.Shutdown(shutdownCtx)
		case <-ticker.C:
		case <-hangup:
		}
	}
}

func validate(args []string) error {
	flags := flag.NewFlagSet("validate", flag.ExitOnError)
	specDir := flags.String("specs", "services", "directory of service spec files")
	haproxyBin := flags.String("haproxy-bin", "", "also run haproxy -c on the generated config")
	_ = flags.Parse(args)

	services, err := spec.LoadDir(*specDir)
	if err != nil {
		return err
	}

	if *haproxyBin != "" {
		cfg := haproxy.Build(services, haproxy.Config{}, 4)
		rendered, err := haproxy.Render(cfg, haproxy.RenderOptions{AdminSocket: "/tmp/lbplane-validate.sock", HTTPPort: 8080, HTTPSPort: 8443})
		if err != nil {
			return err
		}
		path := filepath.Join(os.TempDir(), "lbplane-validate.cfg")
		if err := os.WriteFile(path, rendered, 0o600); err != nil {
			return err
		}
		defer os.Remove(path)
		if err := (haproxy.Client{Binary: *haproxyBin}).Check(context.Background(), path); err != nil {
			return err
		}
	}

	fmt.Printf("%d services valid\n", len(services))
	return nil
}
