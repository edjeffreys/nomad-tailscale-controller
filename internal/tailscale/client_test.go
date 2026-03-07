package tailscale

import (
"encoding/json"
"io"
"net/http"
"net/http/httptest"
"testing"

"go.uber.org/zap"
)

func TestBuildServeConfig_Empty(t *testing.T) {
c := &Client{logger: zap.NewNop()}
cfg := c.buildServeConfig(nil)

if len(cfg.Services) != 0 {
t.Errorf("expected empty Services map, got %v", cfg.Services)
}
}

func TestBuildServeConfig_SingleService(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "sonarr", BackendAddr: "sonarr.service.consul:8989", Port: 443},
}

cfg := c.buildServeConfig(services)

svcCfg, ok := cfg.Services["svc:sonarr"]
if !ok {
t.Fatal("expected service entry for svc:sonarr")
}
handler, ok := svcCfg.TCP[443]
if !ok {
t.Fatal("expected TCP handler for port 443")
}
if handler.TCPForward != "sonarr.service.consul:8989" {
t.Errorf("TCPForward = %q, want %q", handler.TCPForward, "sonarr.service.consul:8989")
}
}

func TestBuildServeConfig_MultipleServices(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "sonarr", BackendAddr: "sonarr:8989", Port: 443},
{Hostname: "radarr", BackendAddr: "radarr:7878", Port: 443},
}

cfg := c.buildServeConfig(services)

if len(cfg.Services) != 2 {
t.Errorf("expected 2 services, got %d", len(cfg.Services))
}
if _, ok := cfg.Services["svc:sonarr"]; !ok {
t.Error("missing svc:sonarr")
}
if _, ok := cfg.Services["svc:radarr"]; !ok {
t.Error("missing svc:radarr")
}
}

func TestBuildServeConfig_CustomPort(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 8443},
}

cfg := c.buildServeConfig(services)

svcCfg := cfg.Services["svc:myapp"]
if _, ok := svcCfg.TCP[8443]; !ok {
t.Error("expected TCP handler for port 8443")
}
}

func TestBuildServeConfig_ZeroPortDefaultsTo443(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 0},
}

cfg := c.buildServeConfig(services)

svcCfg := cfg.Services["svc:myapp"]
if _, ok := svcCfg.TCP[443]; !ok {
t.Error("expected TCP handler for port 443 when Port=0")
}
}

func TestNormalizeConfig(t *testing.T) {
cfg := &ServeConfig{}
if cfg.Services != nil {
t.Fatal("precondition: Services should be nil")
}

normalizeConfig(cfg)

if cfg.Services == nil {
t.Error("expected Services to be non-nil after normalize")
}
}

func TestNormalizeConfig_AlreadyInitialized(t *testing.T) {
cfg := &ServeConfig{
Services: map[string]*ServiceConfig{
"svc:test": {TCP: map[uint16]*TCPPortHandler{443: {TCPForward: "test:80"}}},
},
}

normalizeConfig(cfg)

if len(cfg.Services) != 1 {
t.Error("normalize should not clear existing services")
}
}

func TestServeConfigJSON(t *testing.T) {
cfg := &ServeConfig{
Services: map[string]*ServiceConfig{
"svc:myapp": {
TCP: map[uint16]*TCPPortHandler{
443: {TCPForward: "localhost:3000"},
},
},
},
}

data, err := json.Marshal(cfg)
if err != nil {
t.Fatal(err)
}

jsonStr := string(data)
if !contains(jsonStr, `"svc:myapp"`) {
t.Error("expected svc:myapp in JSON")
}
if !contains(jsonStr, `"TCPForward"`) {
t.Error("expected TCPForward in JSON")
}
if !contains(jsonStr, `"localhost:3000"`) {
t.Error("expected backend addr in JSON")
}
// Must NOT contain version (old format artifact)
if contains(jsonStr, `"version"`) {
t.Error("unexpected version field in JSON")
}

// Round-trip
var decoded ServeConfig
if err := json.Unmarshal(data, &decoded); err != nil {
t.Fatal(err)
}
svcCfg := decoded.Services["svc:myapp"]
if svcCfg == nil {
t.Fatal("svc:myapp missing after round-trip")
}
handler := svcCfg.TCP[443]
if handler == nil || handler.TCPForward != "localhost:3000" {
t.Error("TCPForward should survive round-trip")
}
}

func TestApply_SkipsWhenUnchanged(t *testing.T) {
existingConfig := &ServeConfig{
Services: map[string]*ServiceConfig{
"svc:myapp": {
TCP: map[uint16]*TCPPortHandler{
443: {TCPForward: "myapp:3000"},
},
},
},
}

postCalled := false
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(existingConfig)
return
}
postCalled = true
w.WriteHeader(http.StatusOK)
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 443},
}

if err := c.Apply(services); err != nil {
t.Fatal(err)
}
if postCalled {
t.Error("expected POST to be skipped when config is unchanged")
}
}

func TestApply_PostsWhenChanged(t *testing.T) {
var postedConfig ServeConfig
postCalled := false

mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&ServeConfig{})
return
}
postCalled = true
body, _ := io.ReadAll(r.Body)
json.Unmarshal(body, &postedConfig)
w.WriteHeader(http.StatusOK)
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

services := []Service{
{Hostname: "sonarr", BackendAddr: "sonarr:8989", Port: 443},
}

if err := c.Apply(services); err != nil {
t.Fatal(err)
}
if !postCalled {
t.Fatal("expected POST to be called when config changes")
}

svcCfg, ok := postedConfig.Services["svc:sonarr"]
if !ok {
t.Fatal("expected svc:sonarr in posted config")
}
handler := svcCfg.TCP[443]
if handler == nil || handler.TCPForward != "sonarr:8989" {
t.Errorf("TCPForward = %q, want %q", handler.TCPForward, "sonarr:8989")
}
}

func TestApply_HandlesGetError(t *testing.T) {
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
w.WriteHeader(http.StatusInternalServerError)
w.Write([]byte("internal error"))
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

err := c.Apply([]Service{{Hostname: "test", BackendAddr: "test:80"}})
if err == nil {
t.Fatal("expected error when GET fails")
}
if !contains(err.Error(), "500") {
t.Errorf("error should mention status code, got: %s", err)
}
}

func TestApply_HandlesPostError(t *testing.T) {
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&ServeConfig{})
return
}
w.WriteHeader(http.StatusConflict)
w.Write([]byte("conflict"))
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

err := c.Apply([]Service{{Hostname: "test", BackendAddr: "test:80"}})
if err == nil {
t.Fatal("expected error when POST returns conflict")
}
if !contains(err.Error(), "409") {
t.Errorf("error should mention 409, got: %s", err)
}
}

func TestApply_EmptyServicesNoopWhenAlreadyEmpty(t *testing.T) {
postCalled := false
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
w.Write([]byte("{}"))
return
}
postCalled = true
w.WriteHeader(http.StatusOK)
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

if err := c.Apply(nil); err != nil {
t.Fatal(err)
}
if postCalled {
t.Error("POST should not be called when both current and desired are empty")
}
}

func TestGetConfig_Handles404AsEmpty(t *testing.T) {
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
w.WriteHeader(http.StatusNotFound)
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

cfg, err := c.getConfig()
if err != nil {
t.Fatal(err)
}
if cfg.Services == nil {
t.Error("expected non-nil Services on 404")
}
if len(cfg.Services) != 0 {
t.Errorf("expected empty Services on 404, got %d", len(cfg.Services))
}
}

// rewriteTransport rewrites requests to localAPIBase to point at the test server.
type rewriteTransport struct {
base   http.RoundTripper
target string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
req = req.Clone(req.Context())
req.URL.Scheme = "http"
req.URL.Host = t.target[len("http://"):]
return t.base.RoundTrip(req)
}

func contains(s, substr string) bool {
for i := 0; i <= len(s)-len(substr); i++ {
if s[i:i+len(substr)] == substr {
return true
}
}
return false
}
