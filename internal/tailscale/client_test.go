package tailscale

import (
"encoding/json"
"io"
"net/http"
"net/http/httptest"
"slices"
"sort"
"testing"

"go.uber.org/zap"
)

// defaultPrefsHandler returns a handler that serves empty prefs on GET
// and accepts PATCH to update AdvertiseServices.
func defaultPrefsHandler(advertised *[]string) http.HandlerFunc {
return func(w http.ResponseWriter, r *http.Request) {
switch r.Method {
case http.MethodGet:
	prefs := &Prefs{AdvertiseServices: *advertised}
	json.NewEncoder(w).Encode(prefs)
case http.MethodPatch:
	var mp MaskedPrefs
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &mp)
	if mp.AdvertiseServicesSet {
		*advertised = mp.AdvertiseServices
	}
	json.NewEncoder(w).Encode(&Prefs{AdvertiseServices: *advertised})
default:
	w.WriteHeader(http.StatusMethodNotAllowed)
}
}
}

func TestBuildServeConfig_Empty(t *testing.T) {
c := &Client{tailnet: "tail5f17e.ts.net", logger: zap.NewNop()}
cfg := c.buildServeConfig(nil)

if len(cfg.Services) != 0 {
t.Errorf("expected empty Services map, got %v", cfg.Services)
}
}

func TestBuildServeConfig_SingleService_HTTPS(t *testing.T) {
c := &Client{tailnet: "tail5f17e.ts.net", logger: zap.NewNop()}

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
if !handler.HTTPS {
t.Error("expected HTTPS=true for default proto")
}

hp := HostPort("sonarr.tail5f17e.ts.net:443")
webCfg, ok := svcCfg.Web[hp]
if !ok {
t.Fatalf("expected Web handler for %s", hp)
}
h, ok := webCfg.Handlers["/"]
if !ok {
t.Fatal("expected handler for /")
}
if h.Proxy != "http://sonarr.service.consul:8989" {
t.Errorf("Proxy = %q, want %q", h.Proxy, "http://sonarr.service.consul:8989")
}
}

func TestBuildServeConfig_SingleService_TCP(t *testing.T) {
c := &Client{tailnet: "tail5f17e.ts.net", logger: zap.NewNop()}

services := []Service{
{Hostname: "sonarr", BackendAddr: "sonarr.service.consul:8989", Port: 443, Proto: "tcp"},
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
if len(svcCfg.Web) != 0 {
t.Error("expected no Web handlers for tcp proto")
}
}

func TestBuildServeConfig_MultipleServices(t *testing.T) {
c := &Client{tailnet: "tail5f17e.ts.net", logger: zap.NewNop()}

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
c := &Client{tailnet: "tail5f17e.ts.net", logger: zap.NewNop()}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 8443},
}

cfg := c.buildServeConfig(services)

svcCfg := cfg.Services["svc:myapp"]
if _, ok := svcCfg.TCP[8443]; !ok {
t.Error("expected TCP handler for port 8443")
}
hp := HostPort("myapp.tail5f17e.ts.net:8443")
if _, ok := svcCfg.Web[hp]; !ok {
t.Errorf("expected Web handler for %s", hp)
}
}

func TestBuildServeConfig_ZeroPortDefaultsTo443(t *testing.T) {
c := &Client{tailnet: "tail5f17e.ts.net", logger: zap.NewNop()}

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
443: {HTTPS: true},
},
Web: map[HostPort]*WebServerConfig{
"myapp.tail5f17e.ts.net:443": {
Handlers: map[string]*HTTPHandler{
"/": {Proxy: "http://localhost:3000"},
},
},
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
if !contains(jsonStr, `"HTTPS":true`) {
t.Error("expected HTTPS:true in JSON")
}
if !contains(jsonStr, `"Proxy"`) {
t.Error("expected Proxy in JSON")
}
if !contains(jsonStr, `"http://localhost:3000"`) {
t.Error("expected backend URL in JSON")
}

var decoded ServeConfig
if err := json.Unmarshal(data, &decoded); err != nil {
t.Fatal(err)
}
svcCfg := decoded.Services["svc:myapp"]
if svcCfg == nil {
t.Fatal("svc:myapp missing after round-trip")
}
handler := svcCfg.TCP[443]
if handler == nil || !handler.HTTPS {
t.Error("HTTPS should survive round-trip")
}
webCfg := svcCfg.Web["myapp.tail5f17e.ts.net:443"]
if webCfg == nil {
t.Fatal("Web config missing after round-trip")
}
h := webCfg.Handlers["/"]
if h == nil || h.Proxy != "http://localhost:3000" {
t.Error("Proxy should survive round-trip")
}
}

func TestApply_SkipsWhenUnchanged(t *testing.T) {
existingConfig := &ServeConfig{
Services: map[string]*ServiceConfig{
"svc:myapp": {
TCP: map[uint16]*TCPPortHandler{
443: {HTTPS: true},
},
Web: map[HostPort]*WebServerConfig{
"myapp.tail5f17e.ts.net:443": {
Handlers: map[string]*HTTPHandler{
"/": {Proxy: "http://myapp:3000"},
},
},
},
},
},
}
advertised := []string{"svc:myapp"}

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
mux.HandleFunc("/localapi/v0/prefs", defaultPrefsHandler(&advertised))

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
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
advertised := []string{}

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
mux.HandleFunc("/localapi/v0/prefs", defaultPrefsHandler(&advertised))

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
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
if handler == nil || !handler.HTTPS {
t.Error("expected HTTPS=true in posted config")
}

hp := HostPort("sonarr.tail5f17e.ts.net:443")
webCfg, ok := svcCfg.Web[hp]
if !ok {
t.Fatalf("expected Web handler for %s", hp)
}
h := webCfg.Handlers["/"]
if h == nil || h.Proxy != "http://sonarr:8989" {
t.Errorf("Proxy = %q, want %q", h.Proxy, "http://sonarr:8989")
}

// Verify advertise was called
sort.Strings(advertised)
if !slices.Equal(advertised, []string{"svc:sonarr"}) {
t.Errorf("advertised = %v, want [svc:sonarr]", advertised)
}
}

func TestApply_AdvertisesMultipleServices(t *testing.T) {
advertised := []string{}

mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&ServeConfig{})
return
}
w.WriteHeader(http.StatusOK)
})
mux.HandleFunc("/localapi/v0/prefs", defaultPrefsHandler(&advertised))

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

services := []Service{
{Hostname: "sonarr", BackendAddr: "sonarr:8989", Port: 443},
{Hostname: "mealie", BackendAddr: "mealie:9000", Port: 9000},
}

if err := c.Apply(services); err != nil {
t.Fatal(err)
}

sort.Strings(advertised)
want := []string{"svc:mealie", "svc:sonarr"}
if !slices.Equal(advertised, want) {
t.Errorf("advertised = %v, want %v", advertised, want)
}
}

func TestApply_SkipsAdvertiseWhenAlreadyCurrent(t *testing.T) {
advertised := []string{"svc:myapp"}
patchCalled := false

mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&ServeConfig{
Services: map[string]*ServiceConfig{
"svc:myapp": {
TCP: map[uint16]*TCPPortHandler{443: {HTTPS: true}},
Web: map[HostPort]*WebServerConfig{
"myapp.tail5f17e.ts.net:443": {
Handlers: map[string]*HTTPHandler{
"/": {Proxy: "http://myapp:3000"},
},
},
},
},
},
})
return
}
w.WriteHeader(http.StatusOK)
})
mux.HandleFunc("/localapi/v0/prefs", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&Prefs{AdvertiseServices: advertised})
return
}
patchCalled = true
w.WriteHeader(http.StatusOK)
json.NewEncoder(w).Encode(&Prefs{AdvertiseServices: advertised})
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 443},
}

if err := c.Apply(services); err != nil {
t.Fatal(err)
}
if patchCalled {
t.Error("expected PATCH to be skipped when advertised services are already correct")
}
}

func TestApply_RemovesStaleAdvertisedServices(t *testing.T) {
advertised := []string{"svc:old-service", "svc:myapp"}

mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&ServeConfig{})
return
}
w.WriteHeader(http.StatusOK)
})
mux.HandleFunc("/localapi/v0/prefs", defaultPrefsHandler(&advertised))

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

services := []Service{
{Hostname: "myapp", BackendAddr: "myapp:3000", Port: 443},
}

if err := c.Apply(services); err != nil {
t.Fatal(err)
}

if !slices.Equal(advertised, []string{"svc:myapp"}) {
t.Errorf("advertised = %v, want [svc:myapp] (old-service should be removed)", advertised)
}
}

func TestApply_HandlesGetError(t *testing.T) {
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
w.WriteHeader(http.StatusInternalServerError)
w.Write([]byte("internal error"))
})
mux.HandleFunc("/localapi/v0/prefs", func(w http.ResponseWriter, r *http.Request) {
json.NewEncoder(w).Encode(&Prefs{})
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
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
advertised := []string{}

mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&ServeConfig{})
return
}
w.WriteHeader(http.StatusConflict)
w.Write([]byte("conflict"))
})
mux.HandleFunc("/localapi/v0/prefs", defaultPrefsHandler(&advertised))

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
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

func TestApply_HandlesPrefsGetError(t *testing.T) {
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
json.NewEncoder(w).Encode(&ServeConfig{})
})
mux.HandleFunc("/localapi/v0/prefs", func(w http.ResponseWriter, r *http.Request) {
w.WriteHeader(http.StatusInternalServerError)
w.Write([]byte("prefs error"))
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

err := c.Apply([]Service{{Hostname: "test", BackendAddr: "test:80"}})
if err == nil {
t.Fatal("expected error when prefs GET fails")
}
if !contains(err.Error(), "prefs") {
t.Errorf("error should mention prefs, got: %s", err)
}
}

func TestApply_HandlesPrefsPatchError(t *testing.T) {
mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
json.NewEncoder(w).Encode(&ServeConfig{})
})
mux.HandleFunc("/localapi/v0/prefs", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
json.NewEncoder(w).Encode(&Prefs{})
return
}
w.WriteHeader(http.StatusInternalServerError)
w.Write([]byte("patch failed"))
})

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
logger: zap.NewNop(),
http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
}

err := c.Apply([]Service{{Hostname: "test", BackendAddr: "test:80"}})
if err == nil {
t.Fatal("expected error when prefs PATCH fails")
}
if !contains(err.Error(), "advertise") {
t.Errorf("error should mention advertise, got: %s", err)
}
}

func TestApply_EmptyServicesNoopWhenAlreadyEmpty(t *testing.T) {
postCalled := false
advertised := []string{}

mux := http.NewServeMux()
mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
if r.Method == http.MethodGet {
w.Write([]byte("{}"))
return
}
postCalled = true
w.WriteHeader(http.StatusOK)
})
mux.HandleFunc("/localapi/v0/prefs", defaultPrefsHandler(&advertised))

srv := httptest.NewServer(mux)
t.Cleanup(srv.Close)

c := &Client{
tailnet: "tail5f17e.ts.net",
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
tailnet: "tail5f17e.ts.net",
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

func TestMaskedPrefsJSON(t *testing.T) {
mp := &MaskedPrefs{
Prefs:                Prefs{AdvertiseServices: []string{"svc:mealie", "svc:sonarr"}},
AdvertiseServicesSet: true,
}

data, err := json.Marshal(mp)
if err != nil {
t.Fatal(err)
}

jsonStr := string(data)
if !contains(jsonStr, `"AdvertiseServicesSet":true`) {
t.Errorf("expected AdvertiseServicesSet in JSON, got: %s", jsonStr)
}
if !contains(jsonStr, `"svc:mealie"`) {
t.Errorf("expected svc:mealie in JSON, got: %s", jsonStr)
}

var decoded MaskedPrefs
if err := json.Unmarshal(data, &decoded); err != nil {
t.Fatal(err)
}
if !decoded.AdvertiseServicesSet {
t.Error("AdvertiseServicesSet should survive round-trip")
}
if len(decoded.AdvertiseServices) != 2 {
t.Errorf("expected 2 services, got %d", len(decoded.AdvertiseServices))
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
