package touka

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

var benchmarkStatusCode int

func buildServeHTTPBenchmarkEngine() *Engine {
	engine := New()
	engine.GET("/api/v1/users", func(c *Context) {
		c.Status(http.StatusNoContent)
	})
	engine.GET("/api/v1/users/:id", func(c *Context) {
		c.Status(http.StatusNoContent)
	})
	engine.GET("/api/v1/users/:id/settings", func(c *Context) {
		c.Status(http.StatusNoContent)
	})
	engine.POST("/api/v1/users", func(c *Context) {
		c.Status(http.StatusNoContent)
	})
	return engine
}

func benchmarkServeHTTP(b *testing.B, engine *Engine, method, path string) {
	b.Helper()

	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		b.Fatalf("failed to build request: %v", err)
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		rr = httptest.NewRecorder()
		engine.ServeHTTP(rr, req)
	}

	benchmarkStatusCode = rr.Code
}

func BenchmarkServeHTTP(b *testing.B) {
	engine := buildServeHTTPBenchmarkEngine()

	b.Run("StaticHit", func(b *testing.B) {
		benchmarkServeHTTP(b, engine, http.MethodGet, "/api/v1/users")
	})

	b.Run("NotFound", func(b *testing.B) {
		benchmarkServeHTTP(b, engine, http.MethodGet, "/does/not/exist")
	})

	b.Run("MethodNotAllowed", func(b *testing.B) {
		benchmarkServeHTTP(b, engine, http.MethodDelete, "/api/v1/users")
	})

	b.Run("OptionsAllow", func(b *testing.B) {
		benchmarkServeHTTP(b, engine, http.MethodOptions, "/api/v1/users")
	})

	b.Run("FixedPathRedirect", func(b *testing.B) {
		benchmarkServeHTTP(b, engine, http.MethodGet, "/API/V1/USERS/123/SETTINGS")
	})
}
