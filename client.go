package tailscale

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"

	"go.uber.org/zap"
)

// ServeConfig mirrors the Tailscale serve config JSON format.
type ServeConfig struct {
	TCP map[string]TCPConfig `json:"TCP,omitempty"`
	Web map[string]WebConfig `json:"Web,omitempty"`
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

// Client manages the Tailscale serve config.
type Client struct {
	socket  string
	logger  *zap.Logger
	mu      sync.Mutex
	current *ServeConfig
	tmpDir  string
}

func NewClient(socket string, logger *zap.Logger) *Client {
	return &Client{
		socket: socket,
		logger: logger,
		tmpDir: os.TempDir(),
	}
}

func (c *Client) Apply(services []Service) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	desired := c.buildServeConfig(services)

	if reflect.DeepEqual(c.current, desired) {
		c.logger.Debug("serve config unchanged, skipping apply")
		return nil
	}

	if err := c.applyConfig(desired); err != nil {
		return err
	}

	c.current = desired
	c.logger.Info("serve config applied", zap.Int("services", len(services)))
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

func (c *Client) applyConfig(cfg *ServeConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal serve config: %w", err)
	}

	tmpFile := filepath.Join(c.tmpDir, "tailscale-serve.json")
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write serve config: %w", err)
	}
	defer os.Remove(tmpFile)

	c.logger.Debug("applying serve config", zap.String("config", string(data)))

	cmd := exec.Command("tailscale", "serve", "--config", tmpFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tailscale serve failed: %w\noutput: %s", err, string(out))
	}

	return nil
}
