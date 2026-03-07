package tailscale

import (
"encoding/json"
"io"
"net/http"
"net/http/httptest"
"testing"

"go.uber.org/zap"
)

func TestServiceBackendURL(t *testing.T) {
s := &Service{BackendAddr: "sabnzbd.service.consul:8080"}
want := "http://sabnzbd.service.consul:8080"
if got := s.BackendURL(); got != want {
t.Errorf("BackendURL() = %q, want %q", got, want)
}
}

func TestBuildServicesConfig_Empty(t *testing.T) {
c := &Client{logger: zap.NewNop()}
cfg := c.buildServicesConfig(nil)

if cfg.Version != "0.0.1" {
t.Errorf("Version = %q, want 0.0.1", cfg.Version)
}
if len(cfg.Services) != 0 {
t.Errorf("expected empty Services map, got %v", cfg.Services)
}
}

func TestBuildServicesConfig_SingleService(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "sonarr", BackendAddr: "sonarr.service.consul:8989", Port: 443},
}

cfg := c.buildServicesConfig(services)

svcDef, ok := cfg.Services["svc:sonarr"]
if !ok {
t.Fatal("expected service entry for svc:sonarr")
}
if !svcDef.Advertised {
t.Error("expected Advertised = true")
}
target, ok := svcDef.Endpoints["tcp:443"]
if !ok {
t.Fatal("expected endpoint for tcp:443")
}
if target != "http://sonarr.service.consul:8989" {
t.Errorf("endpoint target = %q, want %q", target, "http://sonarr.service.consul:8989")
}
}

func TestBuildServicesConfig_MultipleServices(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "sonarr", BackendAddr: "sonarr:8989", Port: 443},
{Hostname: "radarr", BackendAddr: "radarr:7878", Port: 443},
}

cfg := c.buildServicesConfig(services)

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

func TestBuildServicesConfig_CustomPort(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 8443},
}

cfg := c.buildServicesConfig(services)

svcDef := cfg.Services["svc:myapp"]
if _, ok := svcDef.Endpoints["tcp:8443"]; !ok {
t.Error("expected endpoint for tcp:8443")
}
}

func TestBuildServicesConfig_ZeroPortDefaultsTo443(t *testing.T) {
c := &Client{logger: zap.NewNop()}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 0},
}

cfg := c.buildServicesConfig(services)

svcDef := cfg.Services["svc:myapp"]
if _, ok := svcDef.Endpoints["tcp:443"]; !ok {
t.Error("expected endpoint for tcp:443 when Port=0")
}
}

func TestNormalizeConfig(t *testing.T) {
cfg := &ServicesConfig{}
if cfg.Services != nil {
t.Fatal("precondition: Services should be nil")
}

normalizeConfig(cfg)

if cfg.Services == nil {
t.Error("expected Services to be non-nil after normalize")
}
}

func TestNormalizeConfig_AlreadyInitialized(t *testing.T) {
cfg := &ServicesConfig{
Services: map[string]*ServiceDef{
"svc:test": {Endpoints: map[string]string{"tcp:443": "http://test:80"}, Advertised: true},
},
}

normalizeConfig(cfg)

if len(cfg.Services) != 1 {
t.Error("normalize should not clear existing services")
}
}

func TestServicesConfigJSON(t *testing.T) {
cfg := &ServicesConfig{
Version: "0.0.1",
Services: map[string]*ServiceDef{
"svc:myapp": {
Endpoints:  map[string]string{"tcp:443": "http://localhost:3000"},
Advertised: true,
},
},
}

data, err := json.Marshal(cfg)
if err != nil {
t.Fatal(err)
}

jsonStr := string(data)
if !contains(jsonStr, `"version":"0.0.1"`) {
t.Error("expected version in JSON")
}
if !contains(jsonStr, `"svc:myapp"`) {
t.Error("expected svc:myapp in JSON")
}
if !contains(jsonStr, `"tcp:443"`) {
t.Error("expected tcp:443 in JSON")
}

// Round-trip
var decoded ServicesConfig
if err := json.Unmarshal(data, &decoded); err != nil {
t.Fatal(err)
}
if decoded.Version != "0.0.1" {
t.Error("version should survive round-trip")
}
svcDef := decoded.Services["svc:myapp"]
if svcDef == nil || svcDef.Endpoints["tcp:443"] != "http://localhost:3000" {
t.Error("endpoint should survive round-trip")
}
}

func TestApply_SkipsWhenUnchanged(t *testing.T) {
existingConfig := &ServicesConfig{
Version: "0.0.1",
Services: map[string]*ServiceDef{
"svc:myapp": {
Endpoints:  map[string]string{"tcp:443": "http://myapp:3000"},
Advertised: true,
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
var postedConfig ServicesConfig
postCalled := false

mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&ServicesConfig{Version: "0.0.1"})
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

svcDef, ok := postedConfig.Services["svc:sonarr"]
if !ok {
t.Fatal("expected svc:sonarr in posted config")
}
if svcDef.Endpoints["tcp:443"] != "http://sonarr:8989" {
t.Errorf("endpoint = %q, want %q", svcDef.Endpoints["tcp:443"], "http://sonarr:8989")
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
json.NewEncoder(w).Encode(&ServicesConfig{Version: "0.0.1"})
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
