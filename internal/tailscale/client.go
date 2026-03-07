package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"

	"go.uber.org/zap"
)

// ServicesConfig matches the Tailscale Services configuration file format.
// See https://tailscale.com/docs/reference/tailscale-services-configuration-file
type ServicesConfig struct {
	Version  string                 `json:"version"`
	Services map[string]*ServiceDef `json:"services,omitempty"`
}

// ServiceDef defines a single Tailscale Service with its endpoint mappings.
type ServiceDef struct {
	Endpoints  map[string]string `json:"endpoints"`
	Advertised bool              `json:"advertised"`
}

type Service struct {
	Hostname    string
	BackendAddr string
	Port        int
	Tag         string // Tailscale ACL tag for the service definition (e.g. "tag:server")
}

func (s *Service) BackendURL() string {
	return fmt.Sprintf("http://%s", s.BackendAddr)
}

// Client manages the Tailscale serve config via the local API.
type Client struct {
	socket string
	logger *zap.Logger
	http   *http.Client
}

func NewClient(socket string, logger *zap.Logger) *Client {
	return &Client{
		socket: socket,
		logger: logger,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

func (c *Client) Apply(services []Service) error {
	desired := c.buildServicesConfig(services)

	current, err := c.getConfig()
	if err != nil {
		return fmt.Errorf("failed to read current serve config: %w", err)
	}

	normalizeConfig(current)
	normalizeConfig(desired)

	if reflect.DeepEqual(current.Services, desired.Services) {
		c.logger.Debug("serve config unchanged, skipping apply")
		return nil
	}

	if err := c.postConfig(desired); err != nil {
		return err
	}

	c.logger.Info("serve config applied", zap.Int("services", len(services)))
	return nil
}

func normalizeConfig(cfg *ServicesConfig) {
	if cfg.Services == nil {
		cfg.Services = make(map[string]*ServiceDef)
	}
	for _, svc := range cfg.Services {
		if svc.Endpoints == nil {
			svc.Endpoints = make(map[string]string)
		}
	}
}

const localAPIBase = "http://local-tailscaled.sock/localapi/v0/serve-config"

func (c *Client) getConfig() (*ServicesConfig, error) {
	resp, err := c.http.Get(localAPIBase)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &ServicesConfig{Version: "0.0.1", Services: make(map[string]*ServiceDef)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("local API returned %d: %s", resp.StatusCode, body)
	}

	var cfg ServicesConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode serve config: %w", err)
	}
	return &cfg, nil
}

func (c *Client) postConfig(cfg *ServicesConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal serve config: %w", err)
	}

	c.logger.Debug("applying serve config", zap.String("config", string(data)))

	req, err := http.NewRequest(http.MethodPost, localAPIBase, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to post serve config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("local API returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

func (c *Client) buildServicesConfig(services []Service) *ServicesConfig {
	cfg := &ServicesConfig{
		Version:  "0.0.1",
		Services: make(map[string]*ServiceDef),
	}

	for _, svc := range services {
		port := svc.Port
		if port == 0 {
			port = 443
		}

		svcName := fmt.Sprintf("svc:%s", svc.Hostname)
		endpointKey := fmt.Sprintf("tcp:%d", port)

		cfg.Services[svcName] = &ServiceDef{
			Endpoints: map[string]string{
				endpointKey: svc.BackendURL(),
			},
			Advertised: true,
		}
	}

	return cfg
}

