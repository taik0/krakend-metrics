// Package metrics defines a set of basic building blocks for instrumenting KakenD gateways
//
// Check the "github.com/devopsfaith/krakend-metrics/gin" and "github.com/devopsfaith/krakend-metrics/mux"
// packages for complete implementations
package metrics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/devopsfaith/krakend/config"
	"github.com/devopsfaith/krakend/logging"
	"github.com/rcrowley/go-metrics"
)

// New creates a new metrics producer
func New(ctx context.Context, e config.ExtraConfig, l logging.Logger) *Metrics {
	registry := metrics.NewPrefixedRegistry("krakend.")

	var cfg *Config
	if tmp, ok := ConfigGetter(e).(*Config); ok {
		cfg = tmp
	}

	m := Metrics{
		Config:         cfg,
		Router:         NewRouterMetrics(&registry),
		Proxy:          NewProxyMetrics(&registry),
		Registry:       &registry,
		latestSnapshot: NewStats(),
	}

	if m.Config != nil {
		m.processMetrics(ctx, m.Config.CollectionTime, logger{l})
	}

	return &m
}

// Namespace is the key to look for extra configuration details
const Namespace = "github_com/devopsfaith/krakend-metrics"

// Config holds if a component is active or not
type Config struct {
	ProxyDisabled   bool `json:"proxy_disabled,omitempty"`
	RouterDisabled  bool `json:"router_disabled,omitempty"`
	BackendDisabled bool `json:"backend_disabled,omitempty"`
	CollectionTime  time.Duration
}

// ConfigGetter implements the config.ConfigGetter interface. It parses the extra config for the
// collectors and returns a defaultCfg if something goes wrong.
func ConfigGetter(e config.ExtraConfig) interface{} {
	v, ok := e[Namespace]
	if !ok {
		return nil
	}

	tmp, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}

	userCfg := new(Config)
	userCfg.CollectionTime = time.Minute
	if collectionTime, ok := tmp["collection_time"]; ok {
		if d, err := time.ParseDuration(collectionTime.(string)); err == nil {
			userCfg.CollectionTime = d
		}
	}
	userCfg.ProxyDisabled = getBool(tmp, "proxy_disabled")
	userCfg.RouterDisabled = getBool(tmp, "router_disabled")
	userCfg.BackendDisabled = getBool(tmp, "backend_disabled")

	return userCfg
}

func getBool(data map[string]interface{}, name string) bool {
	if flag, ok := data[name]; ok {
		if v, ok := flag.(bool); ok {
			return v
		}
	}
	return false
}

// Metrics is the component that manages all the metrics
type Metrics struct {
	// Config is the metrics collector configuration
	Config *Config
	// Proxy is the metrics collector for the proxy package
	Proxy *ProxyMetrics
	// Router is the metrics collector for the router package
	Router *RouterMetrics
	// Registry is the metrics register
	Registry       *metrics.Registry
	latestSnapshot Stats
}

// Snapshot returns the last calculted snapshot
func (m *Metrics) Snapshot() Stats {
	return m.latestSnapshot
}

// TakeSnapshot takes a snapshot of the current state
func (m *Metrics) TakeSnapshot() Stats {
	tmp := NewStats()

	(*m.Registry).Each(func(k string, v interface{}) {
		switch metric := v.(type) {
		case metrics.Counter:
			tmp.Counters[k] = metric.Count()
		case metrics.Gauge:
			tmp.Gauges[k] = metric.Value()
		case metrics.Histogram:
			tmp.Histograms[k] = HistogramData{
				Max:         metric.Max(),
				Min:         metric.Min(),
				Mean:        metric.Mean(),
				Stddev:      metric.StdDev(),
				Variance:    metric.Variance(),
				Percentiles: metric.Percentiles(percentiles),
			}
			metric.Clear()
		}
	})
	return tmp
}

func (m *Metrics) processMetrics(ctx context.Context, d time.Duration, l metrics.Logger) {
	r := metrics.NewPrefixedChildRegistry(*(m.Registry), "service.")

	metrics.RegisterDebugGCStats(r)
	metrics.RegisterRuntimeMemStats(r)

	go metrics.Log(r, d, l)

	go func() {
		ticker := time.NewTicker(d)
		for {
			select {
			case <-ticker.C:
				metrics.CaptureDebugGCStatsOnce(r)
				metrics.CaptureRuntimeMemStatsOnce(r)
				m.Router.Aggregate()
				m.latestSnapshot = m.TakeSnapshot()
			case <-ctx.Done():
				return
			}
		}
	}()
}

var (
	percentiles   = []float64{0.1, 0.25, 0.5, 0.75, 0.9, 0.95, 0.99}
	defaultSample = func() metrics.Sample { return metrics.NewUniformSample(1028) }
)

type logger struct {
	logger logging.Logger
}

func (l logger) Printf(format string, v ...interface{}) {
	l.logger.Debug(strings.TrimRight(fmt.Sprintf(format, v...), "\n"))
}
