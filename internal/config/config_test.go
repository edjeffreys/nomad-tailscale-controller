package config

import (
	"os"
	"testing"
	"time"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	os.Setenv(key, value)
	t.Cleanup(func() {
		if existed {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, existed := os.LookupEnv(key)
	os.Unsetenv(key)
	t.Cleanup(func() {
		if existed {
			os.Setenv(key, old)
		}
	})
}

func TestFromEnv_Defaults(t *testing.T) {
	setEnv(t, "TAILNET", "test.ts.net")
	unsetEnv(t, "NOMAD_ADDR")
	unsetEnv(t, "NOMAD_TOKEN")
	unsetEnv(t, "TAILSCALE_SOCKET")
	unsetEnv(t, "TAG_PREFIX")
	unsetEnv(t, "POLL_INTERVAL")
	unsetEnv(t, "NOMAD_NAMESPACES")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.NomadAddr != "http://localhost:4646" {
		t.Errorf("NomadAddr = %q, want default", cfg.NomadAddr)
	}
	if cfg.TailscaleSocket != "/var/run/tailscale/tailscaled.sock" {
		t.Errorf("TailscaleSocket = %q, want default", cfg.TailscaleSocket)
	}
	if cfg.TagPrefix != "tailscale." {
		t.Errorf("TagPrefix = %q, want default", cfg.TagPrefix)
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s", cfg.PollInterval)
	}
	if len(cfg.NomadNamespaces) != 1 || cfg.NomadNamespaces[0] != "*" {
		t.Errorf("NomadNamespaces = %v, want [*]", cfg.NomadNamespaces)
	}
}

func TestFromEnv_CustomValues(t *testing.T) {
	setEnv(t, "TAILNET", "mytail.ts.net")
	setEnv(t, "NOMAD_ADDR", "http://nomad:4646")
	setEnv(t, "NOMAD_TOKEN", "secret-token")
	setEnv(t, "TAILSCALE_SOCKET", "/tmp/ts.sock")
	setEnv(t, "TAG_PREFIX", "ts.")
	setEnv(t, "POLL_INTERVAL", "30s")
	setEnv(t, "NOMAD_NAMESPACES", "default,production")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.NomadAddr != "http://nomad:4646" {
		t.Errorf("NomadAddr = %q", cfg.NomadAddr)
	}
	if cfg.NomadToken != "secret-token" {
		t.Errorf("NomadToken = %q", cfg.NomadToken)
	}
	if cfg.Tailnet != "mytail.ts.net" {
		t.Errorf("Tailnet = %q", cfg.Tailnet)
	}
	if cfg.TailscaleSocket != "/tmp/ts.sock" {
		t.Errorf("TailscaleSocket = %q", cfg.TailscaleSocket)
	}
	if cfg.TagPrefix != "ts." {
		t.Errorf("TagPrefix = %q", cfg.TagPrefix)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if len(cfg.NomadNamespaces) != 2 || cfg.NomadNamespaces[0] != "default" || cfg.NomadNamespaces[1] != "production" {
		t.Errorf("NomadNamespaces = %v", cfg.NomadNamespaces)
	}
}

func TestFromEnv_MissingTailnet(t *testing.T) {
	unsetEnv(t, "TAILNET")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected error when TAILNET is missing")
	}
}

func TestFromEnv_InvalidPollInterval(t *testing.T) {
	setEnv(t, "TAILNET", "test.ts.net")
	setEnv(t, "POLL_INTERVAL", "not-a-duration")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected error for invalid POLL_INTERVAL")
	}
}

func TestSplitComma(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"single", []string{"single"}},
		{"", []string{""}},
		{"a,,c", []string{"a", "", "c"}},
		{"*", []string{"*"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitComma(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitComma(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("splitComma(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitEnvOrDefault_FiltersEmpty(t *testing.T) {
	setEnv(t, "TEST_SPLIT_KEY", "a,,b,")

	got := splitEnvOrDefault("TEST_SPLIT_KEY", []string{"default"})

	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("splitEnvOrDefault with empty segments = %v, want [a b]", got)
	}
}

func TestSplitEnvOrDefault_ReturnsDefault(t *testing.T) {
	unsetEnv(t, "TEST_SPLIT_NOEXIST")

	got := splitEnvOrDefault("TEST_SPLIT_NOEXIST", []string{"*"})

	if len(got) != 1 || got[0] != "*" {
		t.Errorf("splitEnvOrDefault unset = %v, want [*]", got)
	}
}
