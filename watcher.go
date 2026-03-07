package nomad

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
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

// Watcher watches Nomad for services tagged with tailscale. tags
// and reconciles the Tailscale serve config accordingly.
type Watcher struct {
	cfg      *config.Config
	ts       *tailscale.Client
	logger   *zap.Logger
	nomad    *nomadapi.Client
}

func NewWatcher(cfg *config.Config, ts *tailscale.Client, logger *zap.Logger) *Watcher {
	nomadCfg := nomadapi.DefaultConfig()
	nomadCfg.Address = cfg.NomadAddr
	if cfg.NomadToken != "" {
		nomadCfg.SecretID = cfg.NomadToken
	}

	client, err := nomadapi.NewClient(nomadCfg)
	if err != nil {
		// DefaultConfig never errors, but handle defensively
		panic(fmt.Sprintf("failed to create nomad client: %v", err))
	}

	return &Watcher{
		cfg:    cfg,
		ts:     ts,
		logger: logger,
		nomad:  client,
	}
}

// Run starts the watcher. It does an initial reconciliation then watches
// the Nomad event stream for changes, falling back to polling.
func (w *Watcher) Run(ctx context.Context) error {
	// Initial reconciliation
	if err := w.reconcile(ctx); err != nil {
		w.logger.Error("initial reconciliation failed", zap.Error(err))
	}

	// Watch event stream for immediate updates, poll as fallback
	eventCh := w.watchEvents(ctx)
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case err, ok := <-eventCh:
			if !ok {
				w.logger.Warn("event stream closed, relying on polling")
				eventCh = nil
				continue
			}
			if err != nil {
				w.logger.Warn("event stream error", zap.Error(err))
				continue
			}
			w.logger.Debug("received nomad event, reconciling")
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

// watchEvents subscribes to the Nomad event stream and sends on the channel
// whenever a service-relevant event is received.
func (w *Watcher) watchEvents(ctx context.Context) <-chan error {
	ch := make(chan error, 1)

	go func() {
		defer close(ch)

		topics := map[nomadapi.Topic][]string{
			nomadapi.TopicService: {"*"},
		}

		eventCh, err := w.nomad.EventStream().Stream(ctx, topics, 0, nil)
		if err != nil {
			ch <- fmt.Errorf("failed to subscribe to event stream: %w", err)
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case events, ok := <-eventCh:
				if !ok {
					return
				}
				if events.Err != nil {
					ch <- events.Err
					continue
				}
				// Signal that something changed — reconcile will figure out what
				ch <- nil
			}
		}
	}()

	return ch
}

// reconcile fetches all services from Nomad across configured namespaces,
// filters for tailscale-tagged ones, and applies the serve config.
func (w *Watcher) reconcile(ctx context.Context) error {
	var services []tailscale.Service

	for _, ns := range w.cfg.NomadNamespaces {
		svcs, err := w.fetchServices(ctx, ns)
		if err != nil {
			return fmt.Errorf("failed to fetch services in namespace %q: %w", ns, err)
		}
		services = append(services, svcs...)
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

// fetchServices fetches all Nomad services in a namespace and returns
// those tagged with tailscale.enable=true.
func (w *Watcher) fetchServices(ctx context.Context, namespace string) ([]tailscale.Service, error) {
	q := &nomadapi.QueryOptions{
		Namespace: namespace,
	}
	q = q.WithContext(ctx)

	stubs, _, err := w.nomad.Services().List(q)
	if err != nil {
		return nil, err
	}

	var result []tailscale.Service

	for _, stub := range stubs {
		for _, svc := range stub.Services {
			tags := parseTags(svc.Tags, w.cfg.TagPrefix)

			if tags[tagEnable] != "true" {
				continue
			}

			hostname := tags[tagHostname]
			if hostname == "" {
				hostname = svc.ServiceName
			}

			// Backend defaults to Consul DNS for the service
			backend := tags[tagBackend]
			if backend == "" {
				backend = fmt.Sprintf("%s.service.consul:%d", svc.ServiceName, svc.Port)
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
	}

	return result, nil
}

// parseTags parses Nomad service tags with a given prefix into a map.
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
