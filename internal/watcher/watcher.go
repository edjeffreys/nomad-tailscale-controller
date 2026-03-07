package watcher

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/edjeffreys/nomad-tailscale-controller/internal/config"
	"github.com/edjeffreys/nomad-tailscale-controller/internal/tailscale"
	"go.uber.org/zap"
)

const (
	tagEnable   = "enable"
	tagHostname = "hostname"
	tagPort     = "port"
	tagBackend  = "backend"
)

// Watcher watches Consul for services tagged with tailscale. tags
// and reconciles the Tailscale serve config accordingly.
type Watcher struct {
	cfg    *config.Config
	ts     *tailscale.Client
	logger *zap.Logger
	consul *consulapi.Client
}

func NewWatcher(cfg *config.Config, ts *tailscale.Client, logger *zap.Logger) *Watcher {
	consulCfg := consulapi.DefaultConfig()
	if cfg.ConsulAddr != "" {
		consulCfg.Address = cfg.ConsulAddr
	}
	if cfg.ConsulToken != "" {
		consulCfg.Token = cfg.ConsulToken
	}

	client, err := consulapi.NewClient(consulCfg)
	if err != nil {
		panic(fmt.Sprintf("failed to create consul client: %v", err))
	}

	return &Watcher{
		cfg:    cfg,
		ts:     ts,
		logger: logger,
		consul: client,
	}
}

// Run starts the watcher. It does an initial reconciliation then watches
// Consul using blocking queries for changes, falling back to polling.
func (w *Watcher) Run(ctx context.Context) error {
	// Initial reconciliation
	if err := w.reconcile(ctx); err != nil {
		w.logger.Error("initial reconciliation failed", zap.Error(err))
	}

	// Use Consul blocking queries for immediate updates, poll as fallback
	changeCh := w.watchCatalog(ctx)
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case err, ok := <-changeCh:
			if !ok {
				w.logger.Warn("catalog watch closed, relying on polling")
				changeCh = nil
				continue
			}
			if err != nil {
				w.logger.Warn("catalog watch error", zap.Error(err))
				continue
			}
			w.logger.Debug("consul catalog changed, reconciling")
			if err := w.reconcile(ctx); err != nil {
				w.logger.Error("reconciliation failed", zap.Error(err))
			}

		case <-ticker.C:
			w.logger.Debug("poll tick, reconciling")
			if err := w.reconcile(ctx); err != nil {
				w.logger.Error("reconciliation failed", zap.Error(err))
			}
		}
	}
}

// watchCatalog uses Consul blocking queries to detect catalog changes.
func (w *Watcher) watchCatalog(ctx context.Context) <-chan error {
	ch := make(chan error, 1)

	go func() {
		defer close(ch)

		var lastIndex uint64

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			opts := &consulapi.QueryOptions{
				WaitIndex: lastIndex,
				WaitTime:  5 * time.Minute,
			}
			opts = opts.WithContext(ctx)

			_, meta, err := w.consul.Catalog().Services(opts)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				ch <- fmt.Errorf("consul catalog watch failed: %w", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if meta.LastIndex > lastIndex {
				lastIndex = meta.LastIndex
				ch <- nil
			}
		}
	}()

	return ch
}

// reconcile fetches all services from Consul, filters for tailscale-tagged ones,
// and applies the serve config.
func (w *Watcher) reconcile(ctx context.Context) error {
	services, err := w.fetchServices(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch services: %w", err)
	}

	w.logger.Info("reconciling", zap.Int("tailscale_services", len(services)))
	for _, svc := range services {
		w.logger.Debug("found tailscale service",
			zap.String("hostname", svc.Hostname),
			zap.String("backend", svc.BackendAddr),
		)
	}

	return w.ts.Apply(services)
}

// fetchServices fetches all Consul services and returns those tagged with
// tailscale.enable=true.
func (w *Watcher) fetchServices(ctx context.Context) ([]tailscale.Service, error) {
	opts := (&consulapi.QueryOptions{}).WithContext(ctx)

	// List all services with their tags
	catalog, _, err := w.consul.Catalog().Services(opts)
	if err != nil {
		return nil, err
	}

	w.logger.Debug("fetched consul services",
		zap.Int("total_services", len(catalog)),
	)

	var result []tailscale.Service

	for svcName, svcTags := range catalog {
		w.logger.Debug("evaluating service",
			zap.String("service", svcName),
			zap.Strings("tags", svcTags),
		)

		tags := parseTags(svcTags, w.cfg.TagPrefix)

		if tags[tagEnable] != "true" {
			w.logger.Debug("skipping service: tailscale.enable not set",
				zap.String("service", svcName),
			)
			continue
		}

		hostname := tags[tagHostname]
		if hostname == "" {
			hostname = svcName
		}

		backend := tags[tagBackend]
		if backend == "" {
			// Fetch service instances to get address and port
			instances, _, err := w.consul.Catalog().Service(svcName, "", opts)
			if err != nil || len(instances) == 0 {
				w.logger.Warn("skipping service: could not get instance details",
					zap.String("service", svcName),
					zap.Error(err),
				)
				continue
			}
			inst := instances[0]
			addr := inst.ServiceAddress
			if addr == "" {
				addr = inst.Address
			}
			backend = fmt.Sprintf("%s:%d", addr, inst.ServicePort)
		}

		port := 443
		if p := tags[tagPort]; p != "" {
			if parsed, err := strconv.Atoi(p); err == nil {
				port = parsed
			}
		}

		result = append(result, tailscale.Service{
			Hostname:    hostname,
			Tailnet:     w.cfg.Tailnet,
			BackendAddr: backend,
			Port:        port,
		})
	}

	return result, nil
}

// parseTags parses service tags with a given prefix into a map.
// e.g. "tailscale.enable=true" -> {"enable": "true"}
func parseTags(tags []string, prefix string) map[string]string {
	result := make(map[string]string)
	for _, tag := range tags {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		rest := strings.TrimPrefix(tag, prefix)
		parts := strings.SplitN(rest, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		} else {
			result[parts[0]] = "true"
		}
	}
	return result
}
