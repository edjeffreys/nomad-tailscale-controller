package tailscale

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestServiceFQDN(t *testing.T) {
	s := &Service{Hostname: "myapp", Tailnet: "tail5f17e.ts.net"}
	want := "myapp.tail5f17e.ts.net"
	if got := s.FQDN(); got != want {
		t.Errorf("FQDN() = %q, want %q", got, want)
	}
}

func TestServiceServeKey(t *testing.T) {
	tests := []struct {
		name string
		svc  Service
		want string
	}{
		{
			name: "default port",
			svc:  Service{Hostname: "myapp", Tailnet: "tail5f17e.ts.net", Port: 0},
			want: "myapp.tail5f17e.ts.net:443",
		},
		{
			name: "explicit port 443",
			svc:  Service{Hostname: "myapp", Tailnet: "tail5f17e.ts.net", Port: 443},
			want: "myapp.tail5f17e.ts.net:443",
		},
		{
			name: "custom port",
			svc:  Service{Hostname: "myapp", Tailnet: "tail5f17e.ts.net", Port: 8443},
			want: "myapp.tail5f17e.ts.net:8443",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.svc.ServeKey(); got != tt.want {
				t.Errorf("ServeKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestServiceBackendURL(t *testing.T) {
	s := &Service{BackendAddr: "sabnzbd.service.consul:8080"}
	want := "http://sabnzbd.service.consul:8080"
	if got := s.BackendURL(); got != want {
		t.Errorf("BackendURL() = %q, want %q", got, want)
	}
}

func TestBuildServeConfig_Empty(t *testing.T) {
	logger := zap.NewNop()
	c := &Client{logger: logger}

	cfg := c.buildServeConfig(nil)

	if len(cfg.TCP) != 0 {
		t.Errorf("expected empty TCP map, got %v", cfg.TCP)
	}
	if len(cfg.Web) != 0 {
		t.Errorf("expected empty Web map, got %v", cfg.Web)
	}
}

func TestBuildServeConfig_SingleService(t *testing.T) {
	logger := zap.NewNop()
	c := &Client{logger: logger}

	services := []Service{
		{
			Hostname:    "sonarr",
			Tailnet:     "tail5f17e.ts.net",
			BackendAddr: "sonarr.service.consul:8989",
			Port:        443,
		},
	}

	cfg := c.buildServeConfig(services)

	// TCP: port 443 with HTTPS
	tcp, ok := cfg.TCP["443"]
	if !ok {
		t.Fatal("expected TCP entry for port 443")
	}
	if !tcp.HTTPS {
		t.Error("expected TCP[443].HTTPS = true")
	}

	// Web: hostname:port -> handler
	webKey := "sonarr.tail5f17e.ts.net:443"
	web, ok := cfg.Web[webKey]
	if !ok {
		t.Fatalf("expected Web entry for %q", webKey)
	}
	handler, ok := web.Handlers["/"]
	if !ok {
		t.Fatal("expected handler for /")
	}
	if handler.Proxy != "http://sonarr.service.consul:8989" {
		t.Errorf("handler.Proxy = %q, want %q", handler.Proxy, "http://sonarr.service.consul:8989")
	}
}

func TestBuildServeConfig_MultipleServices(t *testing.T) {
	logger := zap.NewNop()
	c := &Client{logger: logger}

	services := []Service{
		{Hostname: "sonarr", Tailnet: "tail5f17e.ts.net", BackendAddr: "sonarr:8989", Port: 443},
		{Hostname: "radarr", Tailnet: "tail5f17e.ts.net", BackendAddr: "radarr:7878", Port: 443},
	}

	cfg := c.buildServeConfig(services)

	if len(cfg.TCP) != 1 {
		t.Errorf("expected 1 TCP entry (both on 443), got %d", len(cfg.TCP))
	}
	if len(cfg.Web) != 2 {
		t.Errorf("expected 2 Web entries, got %d", len(cfg.Web))
	}
}

func TestBuildServeConfig_CustomPort(t *testing.T) {
	logger := zap.NewNop()
	c := &Client{logger: logger}

	services := []Service{
		{Hostname: "myapp", Tailnet: "tail5f17e.ts.net", BackendAddr: "myapp:3000", Port: 8443},
	}

	cfg := c.buildServeConfig(services)

	if _, ok := cfg.TCP["8443"]; !ok {
		t.Error("expected TCP entry for port 8443")
	}
	if _, ok := cfg.Web["myapp.tail5f17e.ts.net:8443"]; !ok {
		t.Error("expected Web entry for myapp.tail5f17e.ts.net:8443")
	}
}

func TestBuildServeConfig_ZeroPortDefaultsTo443(t *testing.T) {
	logger := zap.NewNop()
	c := &Client{logger: logger}

	services := []Service{
		{Hostname: "myapp", Tailnet: "tail5f17e.ts.net", BackendAddr: "myapp:3000", Port: 0},
	}

	cfg := c.buildServeConfig(services)

	if _, ok := cfg.TCP["443"]; !ok {
		t.Error("expected TCP entry for port 443 when Port=0")
	}
}

func TestNormalizeConfig(t *testing.T) {
	cfg := &ServeConfig{}
	if cfg.TCP != nil || cfg.Web != nil {
		t.Fatal("precondition: maps should be nil")
	}

	normalizeConfig(cfg)

	if cfg.TCP == nil {
		t.Error("expected TCP to be non-nil after normalize")
	}
	if cfg.Web == nil {
		t.Error("expected Web to be non-nil after normalize")
	}
}

func TestNormalizeConfig_AlreadyInitialized(t *testing.T) {
	cfg := &ServeConfig{
		TCP: map[string]TCPConfig{"443": {HTTPS: true}},
		Web: map[string]WebConfig{},
	}

	normalizeConfig(cfg)

	if len(cfg.TCP) != 1 {
		t.Error("normalize should not clear existing TCP entries")
	}
}

func TestServeConfigJSON(t *testing.T) {
	cfg := &ServeConfig{
		TCP: map[string]TCPConfig{"443": {HTTPS: true}},
		Web: map[string]WebConfig{
			"myapp.tail5f17e.ts.net:443": {
				Handlers: map[string]Handler{
					"/": {Proxy: "http://localhost:3000"},
				},
			},
		},
		ETag: "should-not-appear",
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// ETag should be excluded from JSON (json:"-")
	jsonStr := string(data)
	if contains(jsonStr, "should-not-appear") {
		t.Error("ETag should not appear in JSON output")
	}
	if !contains(jsonStr, `"TCP"`) {
		t.Error("expected TCP key in JSON")
	}
	if !contains(jsonStr, `"HTTPS":true`) {
		t.Error("expected HTTPS:true in JSON")
	}

	// Round-trip
	var decoded ServeConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ETag != "" {
		t.Error("ETag should be empty after JSON round-trip")
	}
	if !decoded.TCP["443"].HTTPS {
		t.Error("TCP[443].HTTPS should be true after round-trip")
	}
}

// newTestClient creates a Client backed by an httptest server for the local API.
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{
		logger: zap.NewNop(),
		http:   srv.Client(),
		socket: srv.URL, // not a real socket, but the http.Client routes to the test server
	}
}

func TestApply_SkipsWhenUnchanged(t *testing.T) {
	existingConfig := &ServeConfig{
		TCP: map[string]TCPConfig{"443": {HTTPS: true}},
		Web: map[string]WebConfig{
			"myapp.tail5f17e.ts.net:443": {
				Handlers: map[string]Handler{"/": {Proxy: "http://myapp:3000"}},
			},
		},
	}

	postCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/localapi/v0/serve-config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Etag", `"abc123"`)
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
		http:   srv.Client(),
	}
	// Override the localAPIBase by wrapping the client's http transport
	origGet := c.getConfig
	_ = origGet
	// Simpler: just override the const by making the client talk to the test server
	c.http.Transport = &rewriteTransport{base: srv.Client().Transport, target: srv.URL}

	services := []Service{
		{Hostname: "myapp", Tailnet: "tail5f17e.ts.net", BackendAddr: "myapp:3000", Port: 443},
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
			w.Header().Set("Etag", `"v1"`)
			// Return empty config
			json.NewEncoder(w).Encode(&ServeConfig{})
			return
		}
		postCalled = true
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &postedConfig)

		if r.Header.Get("If-Match") != `"v1"` {
			t.Errorf("expected If-Match header %q, got %q", `"v1"`, r.Header.Get("If-Match"))
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := &Client{
		logger: zap.NewNop(),
		http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
	}

	services := []Service{
		{Hostname: "sonarr", Tailnet: "tail5f17e.ts.net", BackendAddr: "sonarr:8989", Port: 443},
	}

	if err := c.Apply(services); err != nil {
		t.Fatal(err)
	}
	if !postCalled {
		t.Fatal("expected POST to be called when config changes")
	}

	if !postedConfig.TCP["443"].HTTPS {
		t.Error("expected posted config to have TCP[443].HTTPS=true")
	}
	web, ok := postedConfig.Web["sonarr.tail5f17e.ts.net:443"]
	if !ok {
		t.Fatal("expected posted config to have Web entry for sonarr")
	}
	if web.Handlers["/"].Proxy != "http://sonarr:8989" {
		t.Errorf("Proxy = %q, want %q", web.Handlers["/"].Proxy, "http://sonarr:8989")
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

	err := c.Apply([]Service{{Hostname: "test", Tailnet: "test.ts.net", BackendAddr: "test:80"}})
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
		w.Write([]byte("etag mismatch"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	c := &Client{
		logger: zap.NewNop(),
		http:   &http.Client{Transport: &rewriteTransport{base: srv.Client().Transport, target: srv.URL}},
	}

	err := c.Apply([]Service{{Hostname: "test", Tailnet: "test.ts.net", BackendAddr: "test:80"}})
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
			// Return null/empty — after normalize this becomes empty maps
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
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
