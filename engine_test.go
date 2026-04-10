package touka

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestHandleRequestRedirectFixedPath(t *testing.T) {
	engine := New()
	engine.GET("/api/v1/users/:id/settings", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	rr := PerformRequest(engine, http.MethodGet, "/API/V1/USERS/123/SETTINGS", nil, nil)
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("expected fixed-path redirect status %d, got %d", http.StatusMovedPermanently, rr.Code)
	}
	if location := rr.Header().Get("Location"); location != "/api/v1/users/123/settings" {
		t.Fatalf("expected fixed-path redirect location %q, got %q", "/api/v1/users/123/settings", location)
	}
}

func TestHandleRequestSkipsFixedPathLookupForLowercaseMiss(t *testing.T) {
	engine := New()
	engine.GET("/api/v1/users/:id/settings", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	rr := PerformRequest(engine, http.MethodGet, "/does/not/exist", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected lowercase miss to stay as 404, got %d", rr.Code)
	}
}

func TestHandleRequestKeepsFixedPathLookupForUppercaseMiss(t *testing.T) {
	engine := New()
	engine.GET("/Users/Profile", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	rr := PerformRequest(engine, http.MethodGet, "/users/profile", nil, nil)
	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("expected uppercase route miss to trigger fixed-path redirect, got %d", rr.Code)
	}
	if location := rr.Header().Get("Location"); location != "/Users/Profile" {
		t.Fatalf("expected uppercase route redirect location %q, got %q", "/Users/Profile", location)
	}
}

func TestHandleRequestFixedPathLookupMissDoesNotPanic(t *testing.T) {
	engine := New()
	engine.GET("/Users/Profile", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic for fixed-path miss: %v", r)
		}
	}()

	rr := PerformRequest(engine, http.MethodGet, "/users/unknown", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected fixed-path miss to stay as 404, got %d", rr.Code)
	}
}

func TestNoRouteCanContinueToDefaultNotFound(t *testing.T) {
	engine := New()
	engine.NoRoute(func(c *Context) {
		c.Writer.Header().Set("X-NoRoute", "hit")
		c.Next()
	})

	rr := PerformRequest(engine, http.MethodGet, "/missing", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected default not found status %d, got %d", http.StatusNotFound, rr.Code)
	}
	if got := rr.Header().Get("X-NoRoute"); got != "hit" {
		t.Fatalf("expected NoRoute middleware header to be preserved, got %q", got)
	}
}

func TestMethodNotAllowedDoesNotContinueToNoRoute(t *testing.T) {
	engine := New()
	engine.GET("/users", func(c *Context) {
		c.Status(http.StatusNoContent)
	})
	engine.NoRoute(func(c *Context) {
		c.Writer.Header().Set("X-NoRoute", "hit")
		c.Next()
	})

	rr := PerformRequest(engine, http.MethodDelete, "/users", nil, nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected method not allowed status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
	if got := rr.Header().Get("X-NoRoute"); got != "" {
		t.Fatalf("expected NoRoute chain to be skipped after 405, got header %q", got)
	}
}

func TestOptionsAllowHeaderListsMatchingMethods(t *testing.T) {
	engine := New()
	engine.GET("/users", func(c *Context) {
		c.Status(http.StatusNoContent)
	})
	engine.POST("/users", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	rr := PerformRequest(engine, http.MethodOptions, "/users", nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected OPTIONS allow status %d, got %d", http.StatusOK, rr.Code)
	}
	allow := rr.Header().Get("Allow")
	if allow != "GET, POST" && allow != "POST, GET" {
		t.Fatalf("expected Allow header to list matching methods, got %q", allow)
	}
}

func TestDefaultErrorHandleJSONShape(t *testing.T) {
	engine := New()
	rr := PerformRequest(engine, http.MethodGet, "/missing", nil, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}

	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected JSON error body, got %q: %v", rr.Body.String(), err)
	}
	if body.Code != http.StatusNotFound || body.Message != http.StatusText(http.StatusNotFound) || body.Error != "not found" {
		t.Fatalf("unexpected error payload: %+v", body)
	}
}

func TestDefaultMethodNotAllowedJSONShape(t *testing.T) {
	engine := New()
	engine.GET("/users", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	rr := PerformRequest(engine, http.MethodDelete, "/users", nil, nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}

	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected JSON error body, got %q: %v", rr.Body.String(), err)
	}
	if body.Code != http.StatusMethodNotAllowed || body.Message != http.StatusText(http.StatusMethodNotAllowed) || body.Error != "method not allowed" {
		t.Fatalf("unexpected error payload: %+v", body)
	}
}

func TestCustomErrorHandlerStillOverridesDefaultFastPath(t *testing.T) {
	engine := New()
	engine.SetErrorHandler(func(c *Context, code int, err error) {
		c.Writer.Header().Set("X-Custom-Error", "1")
		c.String(code, "custom:%v", err)
	})
	engine.GET("/users", func(c *Context) {
		c.Status(http.StatusNoContent)
	})

	rr := PerformRequest(engine, http.MethodDelete, "/users", nil, nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}
	if got := rr.Header().Get("X-Custom-Error"); got != "1" {
		t.Fatalf("expected custom error header, got %q", got)
	}
	if rr.Body.String() != "custom:method not allowed" {
		t.Fatalf("expected custom error body, got %q", rr.Body.String())
	}
}
