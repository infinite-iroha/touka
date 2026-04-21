package touka

import (
	"bufio"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"testing"
)

type failingResponseWriter struct {
	header http.Header
	status int
	err    error
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failingResponseWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *failingResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.err != nil {
		return 0, w.err
	}
	return len(p), nil
}

func (w *failingResponseWriter) Flush() {}

func (w *failingResponseWriter) Status() int {
	return w.status
}

func (w *failingResponseWriter) Size() int {
	return 0
}

func (w *failingResponseWriter) Written() bool {
	return w.status != 0
}

func (w *failingResponseWriter) IsHijacked() bool {
	return false
}

func (w *failingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}

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

func TestResponseHelpersCaptureWriteErrors(t *testing.T) {
	testCases := []struct {
		name string
		run  func(*Context)
	}{
		{name: "Raw", run: func(c *Context) { c.Raw(http.StatusOK, "application/octet-stream", []byte("payload")) }},
		{name: "String", run: func(c *Context) { c.String(http.StatusOK, "value=%d", 1) }},
		{name: "Text", run: func(c *Context) { c.Text(http.StatusOK, "payload") }},
		{name: "JSONBuf", run: func(c *Context) { c.JSONBuf(http.StatusOK, map[string]string{"a": "b"}) }},
		{name: "GOBBuf", run: func(c *Context) { c.GOBBuf(http.StatusOK, struct{ A string }{A: "b"}) }},
		{name: "WANFBuf", run: func(c *Context) { c.WANFBuf(http.StatusOK, map[string]string{"a": "b"}) }},
		{name: "HTMLFallback", run: func(c *Context) { c.HTML(http.StatusOK, "page", map[string]string{"a": "b"}) }},
		{name: "HTMLBuf", run: func(c *Context) {
			c.engine.HTMLRender = template.Must(template.New("page").Parse(`{{.a}}`))
			c.HTMLBuf(http.StatusOK, "page", map[string]string{"a": "b"})
		}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			writerErr := errors.New("write failed")
			w := &failingResponseWriter{err: writerErr}
			c, _ := CreateTestContext(w)

			tc.run(c)

			if got := len(c.Errors); got != 1 {
				t.Fatalf("expected exactly one captured error, got %d", got)
			}
			if !errors.Is(c.Errors[len(c.Errors)-1], writerErr) {
				t.Fatalf("expected captured error to wrap write failure, got %v", c.Errors[len(c.Errors)-1])
			}
		})
	}
}

func TestDefaultErrorFastPathCapturesWriteErrors(t *testing.T) {
	writerErr := errors.New("write failed")
	w := &failingResponseWriter{err: writerErr}
	engine := New()
	c, _ := CreateTestContext(w)
	c.engine = engine
	req, err := http.NewRequest(http.MethodGet, "/missing", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	c.reset(w, req)

	defaultErrorHandle(c, http.StatusNotFound, errNotFound)

	if len(c.Errors) == 0 {
		t.Fatal("expected write error to be captured")
	}
	if !errors.Is(c.Errors[len(c.Errors)-1], writerErr) {
		t.Fatalf("expected captured error to wrap write failure, got %v", c.Errors[len(c.Errors)-1])
	}
	if c.Writer.Status() != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, c.Writer.Status())
	}
	if !c.IsAborted() {
		t.Fatal("expected fast path to abort context")
	}
}
