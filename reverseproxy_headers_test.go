package touka

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestReverseProxyHeaderOpsAdd(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Custom-Header"); got != "test-value" {
			t.Errorf("expected X-Custom-Header=test-value, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/test", ReverseProxy(ReverseProxyConfig{
		Target: target,
		RequestHeaders: &HeaderOps{
			Add: map[string][]string{
				"X-Custom-Header": {"test-value"},
			},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/test")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestReverseProxyHeaderOpsDelete(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Sensitive") != "" {
			t.Errorf("expected X-Sensitive header to be deleted, got %q", r.Header.Get("X-Sensitive"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/test", ReverseProxy(ReverseProxyConfig{
		Target: target,
		RequestHeaders: &HeaderOps{
			Delete: []string{"X-Sensitive"},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/test", nil)
	req.Header.Set("X-Sensitive", "should-be-removed")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestReverseProxyHeaderOpsSet(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Replace")
		if got != "new-value" {
			t.Errorf("expected X-Replace=new-value, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/test", ReverseProxy(ReverseProxyConfig{
		Target: target,
		RequestHeaders: &HeaderOps{
			Set: map[string][]string{
				"X-Replace": {"new-value"},
			},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/test", nil)
	req.Header.Set("X-Replace", "old-value")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
}

func TestReverseProxyResponseHeaderOps(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "backend-server")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/test", ReverseProxy(ReverseProxyConfig{
		Target: target,
		ResponseHeaders: &RespHeaderOps{
			HeaderOps: &HeaderOps{
				Set: map[string][]string{
					"X-Custom": {"custom-value"},
				},
			},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/test")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if got := resp.Header.Get("X-Custom"); got != "custom-value" {
		t.Errorf("expected X-Custom=custom-value, got %q", got)
	}
}

func TestReverseProxyResponseHeaderOpsDelete(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Powered-By", "Express")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	engine := New()
	engine.GET("/test", ReverseProxy(ReverseProxyConfig{
		Target: target,
		ResponseHeaders: &RespHeaderOps{
			HeaderOps: &HeaderOps{
				Delete: []string{"X-Powered-By"},
			},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/test")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	if got := resp.Header.Get("X-Powered-By"); got != "" {
		t.Errorf("expected X-Powered-By to be deleted, got %q", got)
	}
}
