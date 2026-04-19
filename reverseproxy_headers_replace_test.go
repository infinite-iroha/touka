package touka

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
)

func TestReverseProxyHeaderOpsReplaceSubstring(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Server"); got != "Caddy" {
			t.Errorf("expected X-Server=Caddy, got %q", got)
		}
		if got := r.Header.Get("X-Location"); got != "/api/v2/resource" {
			t.Errorf("expected X-Location=/api/v2/resource, got %q", got)
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
			Replace: map[string][]Replacement{
				"X-Server":   {{Search: "NGINX", Replace: "Caddy"}},
				"X-Location": {{Search: "v1", Replace: "v2"}},
			},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/test", nil)
	req.Header.Set("X-Server", "NGINX")
	req.Header.Set("X-Location", "/api/v1/resource")
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

func TestReverseProxyHeaderOpsReplaceRegexp(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Route"); got != "/proxy-upstream" {
			t.Errorf("expected X-Route=/proxy-upstream, got %q", got)
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
			Replace: map[string][]Replacement{
				"X-Route": {{SearchRegexp: `^/([^/]+)/(.+)$`, Replace: "/proxy-$2"}},
			},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/test", nil)
	req.Header.Set("X-Route", "/original/upstream")
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

func TestReverseProxyHeaderOpsReplaceWildcard(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Host-A"); got != "new.example.com" {
			t.Errorf("expected X-Host-A=new.example.com, got %q", got)
		}
		if got := r.Header.Get("X-Host-B"); got != "new.example.com" {
			t.Errorf("expected X-Host-B=new.example.com, got %q", got)
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
			Replace: map[string][]Replacement{
				"*": {{Search: "old.example.com", Replace: "new.example.com"}},
			},
		},
	}))

	proxy := httptest.NewServer(engine)
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodGet, proxy.URL+"/test", nil)
	req.Header.Set("X-Host-A", "old.example.com")
	req.Header.Set("X-Host-B", "old.example.com")
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

func TestReverseProxyHeaderOpsReplaceResponse(t *testing.T) {
	t.Helper()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "backend-internal:8080")
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
				Replace: map[string][]Replacement{
					"X-Backend": {{Search: "backend-internal:8080", Replace: "public.example.com"}},
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

	if got := resp.Header.Get("X-Backend"); got != "public.example.com" {
		t.Errorf("expected X-Backend=public.example.com, got %q", got)
	}
}

func TestReverseProxyHeaderOpsProvisionInvalidRegexp(t *testing.T) {
	_ = New()
	ReverseProxy(ReverseProxyConfig{
		Target: mustParseURL(t, "http://example.com"),
		RequestHeaders: &HeaderOps{
			Replace: map[string][]Replacement{
				"X-Test": {{SearchRegexp: "[invalid"}},
			},
		},
	})
}

func TestReplacementApply(t *testing.T) {
	tests := []struct {
		name string
		r    *Replacement
		s    string
		want string
	}{
		{name: "nil replacement", r: nil, s: "hello", want: "hello"},
		{name: "empty string", r: &Replacement{Search: "x", Replace: "y"}, s: "", want: ""},
		{name: "substring match", r: &Replacement{Search: "world", Replace: "go"}, s: "hello world", want: "hello go"},
		{name: "substring no match", r: &Replacement{Search: "foo", Replace: "bar"}, s: "hello world", want: "hello world"},
		{name: "substring multiple", r: &Replacement{Search: "a", Replace: "b"}, s: "aaa", want: "bbb"},
		{name: "regexp match", r: &Replacement{SearchRegexp: `\d+`, Replace: "N", re: regexp.MustCompile(`\d+`)}, s: "abc123def", want: "abcNdef"},
		{name: "regexp no match", r: &Replacement{SearchRegexp: `z+`, Replace: "Z", re: regexp.MustCompile(`z+`)}, s: "abc", want: "abc"},
		{name: "empty search and regexp", r: &Replacement{}, s: "unchanged", want: "unchanged"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.apply(tt.s); got != tt.want {
				t.Errorf("Replacement.apply() = %q, want %q", got, tt.want)
			}
		})
	}
}

func BenchmarkHeaderOpsAdd(b *testing.B) {
	ops := &HeaderOps{
		Add: map[string][]string{
			"X-Custom-1": {"value-1"},
			"X-Custom-2": {"value-2"},
			"X-Custom-3": {"value-3"},
		},
	}
	hdr := make(http.Header)
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr = make(http.Header)
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkHeaderOpsSet(b *testing.B) {
	ops := &HeaderOps{
		Set: map[string][]string{
			"X-Frame-Options":    {"DENY"},
			"X-Content-Type-Options": {"nosniff"},
			"X-XSS-Protection":   {"1; mode=block"},
		},
	}
	hdr := make(http.Header)
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr = make(http.Header)
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkHeaderOpsDeleteSingle(b *testing.B) {
	ops := &HeaderOps{
		Delete: []string{"X-Powered-By"},
	}
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr := make(http.Header)
		hdr.Set("X-Powered-By", "Express")
		hdr.Set("X-Keep", "value")
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkHeaderOpsDeleteWildcard(b *testing.B) {
	ops := &HeaderOps{
		Delete: []string{"X-Debug-*"},
	}
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr := make(http.Header)
		hdr.Set("X-Debug-1", "v1")
		hdr.Set("X-Debug-2", "v2")
		hdr.Set("X-Keep", "value")
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkHeaderOpsReplaceSubstring(b *testing.B) {
	ops := &HeaderOps{
		Replace: map[string][]Replacement{
			"Location": {{Search: "http://internal:8080", Replace: "https://public.example.com"}},
		},
	}
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr := make(http.Header)
		hdr.Set("Location", "http://internal:8080/api/v1/users")
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkHeaderOpsReplaceRegexp(b *testing.B) {
	re := regexp.MustCompile(`^http://([^/]+)(/.*)$`)
	ops := &HeaderOps{
		Replace: map[string][]Replacement{
			"Location": {{SearchRegexp: `^http://([^/]+)(/.*)$`, Replace: "https://public.example.com$2", re: re}},
		},
	}
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr := make(http.Header)
		hdr.Set("Location", "http://internal:8080/api/v1/users")
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkHeaderOpsReplaceWildcard(b *testing.B) {
	ops := &HeaderOps{
		Replace: map[string][]Replacement{
			"*": {{Search: "internal.example.com", Replace: "public.example.com"}},
		},
	}
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr := make(http.Header)
		hdr.Set("X-Host", "internal.example.com")
		hdr.Set("X-Origin", "internal.example.com")
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkHeaderOpsMixed(b *testing.B) {
	ops := &HeaderOps{
		Add: map[string][]string{
			"X-Request-ID": {"req-123"},
		},
		Set: map[string][]string{
			"X-Frame-Options": {"DENY"},
		},
		Delete: []string{"X-Powered-By"},
		Replace: map[string][]Replacement{
			"Location": {{Search: "http://internal:8080", Replace: "https://public.example.com"}},
		},
	}
	repl := &reverseProxyReplacer{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hdr := make(http.Header)
		hdr.Set("X-Powered-By", "Express")
		hdr.Set("Location", "http://internal:8080/api")
		ops.applyTo(hdr, repl)
	}
}

func BenchmarkReplacementApplySubstring(b *testing.B) {
	r := &Replacement{Search: "old.example.com", Replace: "new.example.com"}
	s := "https://old.example.com/api/v1/resource"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.apply(s)
	}
}

func BenchmarkReplacementApplyRegexp(b *testing.B) {
	r := &Replacement{SearchRegexp: `^https?://[^/]+`, Replace: "https://new.example.com", re: regexp.MustCompile(`^https?://[^/]+`)}
	s := "https://old.example.com/api/v1/resource"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = r.apply(s)
	}
}
