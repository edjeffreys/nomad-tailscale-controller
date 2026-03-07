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
unsetEnv(t, "CONSUL_HTTP_ADDR")
unsetEnv(t, "CONSUL_HTTP_TOKEN")
unsetEnv(t, "TAILSCALE_SOCKET")
unsetEnv(t, "TAG_PREFIX")
unsetEnv(t, "POLL_INTERVAL")

cfg, err := FromEnv()
if err != nil {
t.Fatal(err)
}

if cfg.ConsulAddr != "http://localhost:8500" {
t.Errorf("ConsulAddr = %q, want default", cfg.ConsulAddr)
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
}

func TestFromEnv_CustomValues(t *testing.T) {
setEnv(t, "TAILNET", "mytail.ts.net")
setEnv(t, "CONSUL_HTTP_ADDR", "http://consul:8500")
setEnv(t, "CONSUL_HTTP_TOKEN", "secret-token")
setEnv(t, "TAILSCALE_SOCKET", "/tmp/ts.sock")
setEnv(t, "TAG_PREFIX", "ts.")
setEnv(t, "POLL_INTERVAL", "30s")

cfg, err := FromEnv()
if err != nil {
t.Fatal(err)
}

if cfg.ConsulAddr != "http://consul:8500" {
t.Errorf("ConsulAddr = %q", cfg.ConsulAddr)
}
if cfg.ConsulToken != "secret-token" {
t.Errorf("ConsulToken = %q", cfg.ConsulToken)
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
