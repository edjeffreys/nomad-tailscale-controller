package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	NomadAddr string

	NomadToken string

	// NomadNamespaces is a comma-separated list of namespaces to watch.
	// Defaults to "*" (all namespaces).
	NomadNamespaces []string

	// Tailnet is the tailnet domain e.g. tail5f17e.ts.net
	Tailnet string

	// TailscaleSocket is the path to the Tailscale daemon socket.
	// Defaults to /var/run/tailscale/tailscaled.sock
	TailscaleSocket string

	// PollInterval is how often to poll Nomad for service changes.
	// Defaults to 10s. The Nomad event stream is also used for immediate updates.
	PollInterval time.Duration

	// TagPrefix is the tag prefix to look for on Nomad services.
	// Defaults to "tailscale."
	TagPrefix string
}

func FromEnv() (*Config, error) {
	cfg := &Config{
		NomadAddr:       getEnvOrDefault("NOMAD_ADDR", "http://localhost:4646"),
		NomadToken:      os.Getenv("NOMAD_TOKEN"),
		Tailnet:         os.Getenv("TAILNET"),
		TailscaleSocket: getEnvOrDefault("TAILSCALE_SOCKET", "/var/run/tailscale/tailscaled.sock"),
		TagPrefix:       getEnvOrDefault("TAG_PREFIX", "tailscale."),
		NomadNamespaces: splitEnvOrDefault("NOMAD_NAMESPACES", []string{"*"}),
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

func splitEnvOrDefault(key string, def []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var result []string
	for _, s := range splitComma(v) {
		if s != "" {
			result = append(result, s)
		}
	}
	return result
}

func splitComma(s string) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}
