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
	tagTag      = "tag"
	tagProto    = "proto"
)

// Watcher watches Consul for services tagged with tailscale. tags
// and reconciles the Tailscale serve config accordingly.
type Watcher struct {
	cfg    *config.Config
	ts     *tailscale.Client
	api    *tailscale.APIClient // nil if no OAuth creds configured
	logger *zap.Logger
	consul *consulapi.Client
}

func NewWatcher(cfg *config.Config, ts *tailscale.Client, api *tailscale.APIClient, logger *zap.Logger) *Watcher {
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
		api:    api,
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
// ensures they exist in the Tailscale control plane, and applies the serve config.
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
			zap.Int("port", svc.Port),
			zap.String("tag", svc.Tag),
		)
	}

	// Ensure service definitions exist in the control plane (if API client is configured)
	if w.api != nil {
		if err := w.ensureAPIServices(ctx, services); err != nil {
			w.logger.Error("failed to ensure API services", zap.Error(err))
			// Continue to apply local config even if API calls fail
		}
	}

	return w.ts.Apply(services)
}

// ensureAPIServices creates Tailscale VIP Service definitions via the control plane API.
// Each service is checked individually — if it already exists, the PUT is skipped.
func (w *Watcher) ensureAPIServices(ctx context.Context, services []tailscale.Service) error {
	for _, svc := range services {
		svcName := fmt.Sprintf("svc:%s", svc.Hostname)

		port := svc.Port
		if port == 0 {
			port = 443
		}

		apiSvc := tailscale.VIPService{
			Name:    svcName,
			Comment: "Managed by nomad-tailscale-controller",
			Ports:   []string{fmt.Sprintf("tcp:%d", port)},
			Annotations: map[string]string{
				"nomad-tailscale-controller/managed": "true",
			},
		}
		if svc.Tag != "" {
			apiSvc.Tags = []string{svc.Tag}
		}

		if err := w.api.EnsureService(ctx, apiSvc); err != nil {
			w.logger.Error("failed to ensure service", zap.String("service", svcName), zap.Error(err))
			continue
		}
	}

	return nil
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

		// Consul Connect sidecar proxies inherit parent tags — skip them
		if isSidecarProxy(svcName) {
			w.logger.Debug("skipping sidecar proxy",
				zap.String("service", svcName),
			)
			continue
		}

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
		servicePort := 0
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
			servicePort = inst.ServicePort

			// Prefer Consul Connect virtual address (mesh routing via Envoy).
			// Falls back to direct address for non-mesh services.
			if va, ok := inst.ServiceTaggedAddresses["consul-virtual"]; ok {
				addr := va.Address
				port := va.Port
				if port == 0 {
					port = servicePort
				}
				servicePort = port
				backend = fmt.Sprintf("%s:%d", addr, port)
				w.logger.Debug("using consul virtual address",
					zap.String("service", svcName),
					zap.String("backend", backend),
				)
			} else {
				addr := inst.ServiceAddress
				if addr == "" {
					addr = inst.Address
				}
				backend = fmt.Sprintf("%s:%d", addr, servicePort)
			}
		}

		// Default protocol: HTTPS with TLS termination by Tailscale.
		// Use tailscale.proto=tcp for raw TCP forwarding.
		proto := tags[tagProto]
		if proto == "" {
			proto = "https"
		}

		// Default frontend port: 443 for HTTPS, backend's actual port for TCP
		port := servicePort
		if proto == "https" {
			port = 443
		} else if port == 0 {
			port = 443
		}
		if p := tags[tagPort]; p != "" {
			if parsed, err := strconv.Atoi(p); err == nil {
				port = parsed
			}
		}

		tag := tags[tagTag]
		if tag == "" {
			tag = w.cfg.TSDefaultTag
		}

		result = append(result, tailscale.Service{
			Hostname:    hostname,
			BackendAddr: backend,
			Port:        port,
			Proto:       proto,
			Tag:         tag,
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

// isSidecarProxy returns true for Consul Connect sidecar proxy services,
// which inherit their parent's tags and should not be treated as separate services.
func isSidecarProxy(name string) bool {
	return strings.HasSuffix(name, "-sidecar-proxy")
}
