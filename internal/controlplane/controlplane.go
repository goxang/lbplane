package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/goxang/lbplane/internal/haproxy"
	"github.com/goxang/lbplane/internal/spec"
)

type HAProxy interface {
	Check(ctx context.Context, configPath string) error
	Apply(ctx context.Context, commands []string) error
	Reload(ctx context.Context) error
}

type Options struct {
	SpecDir    string
	ConfigPath string
	HostsPath  string
	VIP        string
	MinSlots   int
	Render     haproxy.RenderOptions
}

type ControlPlane struct {
	opts    Options
	haproxy HAProxy
	metrics *Metrics
	logger  *slog.Logger
	applied *haproxy.Config
}

func New(opts Options, lb HAProxy, metrics *Metrics, logger *slog.Logger) *ControlPlane {
	return &ControlPlane{opts: opts, haproxy: lb, metrics: metrics, logger: logger}
}

func (cp *ControlPlane) Reconcile(ctx context.Context) error {
	services, err := spec.LoadDir(cp.opts.SpecDir)
	if err != nil {
		cp.metrics.failures.WithLabelValues("spec").Inc()
		return fmt.Errorf("loading specs, keeping last applied config: %w", err)
	}

	var previous haproxy.Config
	if cp.applied != nil {
		previous = *cp.applied
	}
	next := haproxy.Build(services, previous, cp.opts.MinSlots)

	if cp.applied != nil && haproxy.SameShape(previous, next) {
		commands := haproxy.RuntimeCommands(previous, next)
		if len(commands) == 0 {
			return nil
		}
		err := cp.applyRuntime(ctx, next, commands)
		if err == nil {
			return nil
		}
		// A partial apply leaves unknown state, reload resets it.
		cp.logger.Warn("runtime update failed, falling back to reload", "error", err)
	}

	if err := cp.writeConfig(ctx, next); err != nil {
		return err
	}
	if err := cp.haproxy.Reload(ctx); err != nil {
		if !errors.Is(err, haproxy.ErrNotRunning) {
			cp.metrics.failures.WithLabelValues("reload").Inc()
			return fmt.Errorf("reloading haproxy: %w", err)
		}
		cp.logger.Warn("haproxy is not running yet, it will load the written config on start")
	} else {
		cp.metrics.reloads.Inc()
		cp.logger.Info("haproxy reloaded", "services", len(next.Services))
	}
	return cp.commit(next)
}

func (cp *ControlPlane) applyRuntime(ctx context.Context, next haproxy.Config, commands []string) error {
	if err := cp.haproxy.Apply(ctx, commands); err != nil {
		cp.metrics.failures.WithLabelValues("runtime").Inc()
		return err
	}
	cp.metrics.runtimeUpdates.Add(float64(len(commands)))
	cp.logger.Info("backends updated without reload", "commands", len(commands))

	// Persist so a later reload doesn't revert it.
	if err := cp.writeConfig(ctx, next); err != nil {
		return err
	}
	return cp.commit(next)
}

func (cp *ControlPlane) writeConfig(ctx context.Context, cfg haproxy.Config) error {
	rendered, err := haproxy.Render(cfg, cp.opts.Render)
	if err != nil {
		return fmt.Errorf("rendering config: %w", err)
	}
	candidate := cp.opts.ConfigPath + ".candidate"
	if err := os.WriteFile(candidate, rendered, 0o644); err != nil {
		return err
	}
	defer os.Remove(candidate)

	if err := cp.haproxy.Check(ctx, candidate); err != nil {
		cp.metrics.failures.WithLabelValues("validate").Inc()
		return fmt.Errorf("rendered config rejected, keeping current one: %w", err)
	}
	return os.Rename(candidate, cp.opts.ConfigPath)
}

func (cp *ControlPlane) commit(cfg haproxy.Config) error {
	cp.applied = &cfg
	cp.metrics.services.Set(float64(len(cfg.Services)))
	if cp.opts.HostsPath == "" {
		return nil
	}
	if err := writeFileAtomic(cp.opts.HostsPath, renderHosts(cfg, cp.opts.VIP)); err != nil {
		cp.metrics.failures.WithLabelValues("dns").Inc()
		return fmt.Errorf("writing dns hosts file: %w", err)
	}
	return nil
}

func renderHosts(cfg haproxy.Config, vip string) []byte {
	var b strings.Builder
	for _, svc := range cfg.Services {
		fmt.Fprintf(&b, "%s %s\n", vip, strings.Join(svc.Hostnames, " "))
	}
	return []byte(b.String())
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lbplane-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
