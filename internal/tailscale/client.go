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

// ServeConfig mirrors the Tailscale serve config JSON format.
type ServeConfig struct {
	TCP  map[string]TCPConfig `json:"TCP,omitempty"`
	Web  map[string]WebConfig `json:"Web,omitempty"`
	ETag string               `json:"-"` // populated from response header, not the JSON body
}

type TCPConfig struct {
	HTTPS bool `json:"HTTPS,omitempty"`
}

type WebConfig struct {
	Handlers map[string]Handler `json:"Handlers"`
}

type Handler struct {
	Proxy string `json:"Proxy"`
}

type Service struct {
	Hostname string

	// Tailnet is the tailnet domain e.g. "tail5f17e.ts.net"
	Tailnet string

	// BackendAddr is the Consul DNS address and port e.g. "sabnzbd.service.consul:8080"
	BackendAddr string

	Port int
}

func (s *Service) FQDN() string {
	return fmt.Sprintf("%s.%s", s.Hostname, s.Tailnet)
}

func (s *Service) ServeKey() string {
	port := s.Port
	if port == 0 {
		port = 443
	}
	return fmt.Sprintf("%s:%d", s.FQDN(), port)
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
	desired := c.buildServeConfig(services)

	current, err := c.getConfig()
	if err != nil {
		return fmt.Errorf("failed to read current serve config: %w", err)
	}

	// Normalize nil maps so DeepEqual treats nil and empty maps the same
	normalizeConfig(current)
	normalizeConfig(desired)

	currentCopy := *current
	currentCopy.ETag = ""
	if reflect.DeepEqual(&currentCopy, desired) {
		c.logger.Debug("serve config unchanged, skipping apply")
		return nil
	}

	// Carry the ETag for optimistic concurrency on the POST
	desired.ETag = current.ETag
	if err := c.postConfig(desired); err != nil {
		return err
	}

	c.logger.Info("serve config applied", zap.Int("services", len(services)))
	return nil
}

func normalizeConfig(cfg *ServeConfig) {
	if cfg.TCP == nil {
		cfg.TCP = make(map[string]TCPConfig)
	}
	if cfg.Web == nil {
		cfg.Web = make(map[string]WebConfig)
	}
}

const localAPIBase = "http://local-tailscaled.sock/localapi/v0/serve-config"

func (c *Client) getConfig() (*ServeConfig, error) {
	resp, err := c.http.Get(localAPIBase)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("local API returned %d: %s", resp.StatusCode, body)
	}

	var cfg ServeConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode serve config: %w", err)
	}
	cfg.ETag = resp.Header.Get("Etag")
	return &cfg, nil
}

func (c *Client) postConfig(cfg *ServeConfig) error {
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
	if cfg.ETag != "" {
		req.Header.Set("If-Match", cfg.ETag)
	}

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

func (c *Client) buildServeConfig(services []Service) *ServeConfig {
	cfg := &ServeConfig{
		TCP: make(map[string]TCPConfig),
		Web: make(map[string]WebConfig),
	}

	for _, svc := range services {
		port := svc.Port
		if port == 0 {
			port = 443
		}

		tcpKey := fmt.Sprintf("%d", port)
		cfg.TCP[tcpKey] = TCPConfig{HTTPS: true}

		cfg.Web[svc.ServeKey()] = WebConfig{
			Handlers: map[string]Handler{
				"/": {Proxy: svc.BackendURL()},
			},
		}
	}

	return cfg
}

