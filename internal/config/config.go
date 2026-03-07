package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	// ConsulAddr is the address of the Consul agent.
	// Defaults to CONSUL_HTTP_ADDR or "http://localhost:8500".
	ConsulAddr string

	// ConsulToken is the Consul ACL token.
	// Defaults to CONSUL_HTTP_TOKEN.
	ConsulToken string

	// Tailnet is the tailnet domain e.g. tail5f17e.ts.net
	Tailnet string

	// TailscaleSocket is the path to the Tailscale daemon socket.
	// Defaults to /var/run/tailscale/tailscaled.sock
	TailscaleSocket string

	// TSOAuthClientID is the OAuth2 client ID for the Tailscale API.
	// When set (with TSOAuthClientSecret), the controller will auto-create
	// service definitions in the Tailscale control plane.
	TSOAuthClientID string

	// TSOAuthClientSecret is the OAuth2 client secret for the Tailscale API.
	TSOAuthClientSecret string

	// TSDefaultTag is the Tailscale ACL tag applied to auto-created services.
	// Defaults to "tag:server". Can be overridden per-service via tailscale.tag= Consul tag.
	TSDefaultTag string

	// PollInterval is how often to poll Consul for service changes.
	// Defaults to 10s. Consul blocking queries are also used for immediate updates.
	PollInterval time.Duration

	// TagPrefix is the tag prefix to look for on Consul services.
	// Defaults to "tailscale."
	TagPrefix string
}

func FromEnv() (*Config, error) {
	cfg := &Config{
		ConsulAddr:          getEnvOrDefault("CONSUL_HTTP_ADDR", "http://localhost:8500"),
		ConsulToken:         os.Getenv("CONSUL_HTTP_TOKEN"),
		Tailnet:             os.Getenv("TAILNET"),
		TailscaleSocket:     getEnvOrDefault("TAILSCALE_SOCKET", "/var/run/tailscale/tailscaled.sock"),
		TSOAuthClientID:     os.Getenv("TS_OAUTH_CLIENT_ID"),
		TSOAuthClientSecret: os.Getenv("TS_OAUTH_CLIENT_SECRET"),
		TSDefaultTag:        getEnvOrDefault("TS_DEFAULT_TAG", "tag:server"),
		TagPrefix:           getEnvOrDefault("TAG_PREFIX", "tailscale."),
	}

	pollInterval := getEnvOrDefault("POLL_INTERVAL", "10s")
	d, err := time.ParseDuration(pollInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid POLL_INTERVAL %q: %w", pollInterval, err)
	}
	cfg.PollInterval = d

	if cfg.Tailnet == "" {
		return nil, fmt.Errorf("TAILNET is required (e.g. tail5f17e.ts.net)")
	}

	return cfg, nil
}

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
