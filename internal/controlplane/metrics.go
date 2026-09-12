package controlplane

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	reloads        prometheus.Counter
	runtimeUpdates prometheus.Counter
	failures       *prometheus.CounterVec
	services       prometheus.Gauge
}

func NewMetrics(registry prometheus.Registerer) *Metrics {
	m := &Metrics{
		reloads: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "lbplane_haproxy_reloads_total",
			Help: "HAProxy reloads triggered by config shape changes.",
		}),
		runtimeUpdates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "lbplane_runtime_commands_total",
			Help: "Backend changes applied through the HAProxy runtime API without a reload.",
		}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "lbplane_reconcile_failures_total",
			Help: "Failed reconciliations by stage.",
		}, []string{"stage"}),
		services: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "lbplane_services",
			Help: "Services in the last applied config.",
		}),
	}
	registry.MustRegister(m.reloads, m.runtimeUpdates, m.failures, m.services)
	return m
}
