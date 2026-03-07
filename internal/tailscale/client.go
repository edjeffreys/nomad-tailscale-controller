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
	"slices"
	"sort"

	"go.uber.org/zap"
)

// ServeConfig mirrors the ipn.ServeConfig struct from the Tailscale codebase.
// Only the Services field is used; TCP/Web at the top level are for node-local serve.
type ServeConfig struct {
	TCP      map[uint16]*TCPPortHandler        `json:",omitempty"`
	Web      map[HostPort]*WebServerConfig     `json:",omitempty"`
	Services map[string]*ServiceConfig         `json:",omitempty"`
}

// ServiceConfig mirrors ipn.ServiceConfig — L4/L7 forwarding for a single service.
type ServiceConfig struct {
	TCP map[uint16]*TCPPortHandler    `json:",omitempty"`
	Web map[HostPort]*WebServerConfig `json:",omitempty"`
}

// TCPPortHandler describes what to do with a TCP connection on a given port.
type TCPPortHandler struct {
	HTTPS        bool   `json:",omitempty"`
	HTTP         bool   `json:",omitempty"`
	TCPForward   string `json:",omitempty"`
	TerminateTLS string `json:",omitempty"`
}

// HostPort is an "SNI:port" string, e.g. "myhost.tail1234.ts.net:443".
type HostPort string

// WebServerConfig describes HTTP handler routing.
type WebServerConfig struct {
	Handlers map[string]*HTTPHandler `json:",omitempty"`
}

// HTTPHandler is a single HTTP mount-point handler.
type HTTPHandler struct {
	Path     string `json:",omitempty"`
	Proxy    string `json:",omitempty"`
	Text     string `json:",omitempty"`
	Redirect string `json:",omitempty"`
}

// Prefs is a subset of ipn.Prefs containing only the fields we need.
type Prefs struct {
	AdvertiseServices []string `json:",omitempty"`
}

// MaskedPrefs mirrors ipn.MaskedPrefs for the AdvertiseServices field.
// The embedded Prefs contains the values; the Set bool indicates which fields to apply.
type MaskedPrefs struct {
	Prefs
	AdvertiseServicesSet bool `json:",omitempty"`
}

// Service is our internal representation of a discovered Consul service
// that should be exposed via Tailscale.
type Service struct {
	Hostname    string
	BackendAddr string // host:port of the backend
	Port        int    // frontend port to expose on the Tailscale service
	Tag         string // Tailscale ACL tag (e.g. "tag:server")
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

	normalizeConfig(current)
	normalizeConfig(desired)

	// Compute the desired list of service names to advertise.
	desiredNames := make([]string, 0, len(services))
	for _, svc := range services {
		desiredNames = append(desiredNames, fmt.Sprintf("svc:%s", svc.Hostname))
	}
	sort.Strings(desiredNames)

	// Update advertised services via prefs API.
	prefs, err := c.getPrefs()
	if err != nil {
		return fmt.Errorf("failed to read current prefs: %w", err)
	}

	currentNames := make([]string, len(prefs.AdvertiseServices))
	copy(currentNames, prefs.AdvertiseServices)
	sort.Strings(currentNames)

	if !slices.Equal(currentNames, desiredNames) {
		if err := c.setAdvertiseServices(desiredNames); err != nil {
			return fmt.Errorf("failed to advertise services: %w", err)
		}
		c.logger.Info("advertised services updated", zap.Strings("services", desiredNames))
	}

	// Update serve config.
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

func normalizeConfig(cfg *ServeConfig) {
	if cfg.Services == nil {
		cfg.Services = make(map[string]*ServiceConfig)
	}
	for _, svc := range cfg.Services {
		if svc.TCP == nil {
			svc.TCP = make(map[uint16]*TCPPortHandler)
		}
	}
}

const localAPIBase = "http://local-tailscaled.sock/localapi/v0/serve-config"
const prefsAPIBase = "http://local-tailscaled.sock/localapi/v0/prefs"

func (c *Client) getPrefs() (*Prefs, error) {
	resp, err := c.http.Get(prefsAPIBase)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("prefs API returned %d: %s", resp.StatusCode, body)
	}

	var prefs Prefs
	if err := json.NewDecoder(resp.Body).Decode(&prefs); err != nil {
		return nil, fmt.Errorf("failed to decode prefs: %w", err)
	}
	return &prefs, nil
}

func (c *Client) setAdvertiseServices(svcNames []string) error {
	mp := &MaskedPrefs{
		Prefs:                Prefs{AdvertiseServices: svcNames},
		AdvertiseServicesSet: true,
	}

	data, err := json.Marshal(mp)
	if err != nil {
		return fmt.Errorf("failed to marshal prefs: %w", err)
	}

	c.logger.Debug("setting advertise services", zap.String("prefs", string(data)))

	req, err := http.NewRequest(http.MethodPatch, prefsAPIBase, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create prefs request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to patch prefs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("prefs API returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

func (c *Client) getConfig() (*ServeConfig, error) {
	resp, err := c.http.Get(localAPIBase)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &ServeConfig{Services: make(map[string]*ServiceConfig)}, nil
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("local API returned %d: %s", resp.StatusCode, body)
	}

	var cfg ServeConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to decode serve config: %w", err)
	}
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
		Services: make(map[string]*ServiceConfig),
	}

	for _, svc := range services {
		port := uint16(svc.Port)
		if port == 0 {
			port = 443
		}

		svcName := fmt.Sprintf("svc:%s", svc.Hostname)
		cfg.Services[svcName] = &ServiceConfig{
			TCP: map[uint16]*TCPPortHandler{
				port: {TCPForward: svc.BackendAddr},
			},
		}
	}

	return cfg
}

