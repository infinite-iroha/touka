package touka

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileTextUsesProvidedStatusCode(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(filePath, []byte("hello touka"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	rr := httptest.NewRecorder()
	c, _ := CreateTestContext(rr)

	c.FileText(http.StatusCreated, filePath)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if body := rr.Body.String(); body != "hello touka" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestMaxBytesReaderAllowsExactLimit(t *testing.T) {
	t.Helper()

	reader := NewMaxBytesReader(io.NopCloser(strings.NewReader("abcd")), 4)
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("expected exact limit read to succeed, got %v", err)
	}
	if string(data) != "abcd" {
		t.Fatalf("unexpected data: %q", string(data))
	}
}

func TestMaxBytesReaderRejectsOverLimit(t *testing.T) {
	t.Helper()

	reader := NewMaxBytesReader(io.NopCloser(strings.NewReader("abcde")), 4)
	defer reader.Close()

	_, err := io.ReadAll(reader)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}
}

func TestShouldBindJSONHonorsMaxRequestBodySize(t *testing.T) {
	t.Helper()

	body := strings.NewReader(`{"name":"abcdef"}`)
	req := httptest.NewRequest(http.MethodPost, "/json", body)
	req.Header.Set("Content-Type", "application/json")

	c, _ := CreateTestContextWithRequest(httptest.NewRecorder(), req)
	c.SetMaxRequestBodySize(8)

	var payload struct {
		Name string `json:"name"`
	}

	err := c.ShouldBindJSON(&payload)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}
}

func TestShouldBindFormHonorsMaxRequestBodySize(t *testing.T) {
	t.Helper()

	body := strings.NewReader("name=abcdef")
	req := httptest.NewRequest(http.MethodPost, "/form", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	c, _ := CreateTestContextWithRequest(httptest.NewRecorder(), req)
	c.SetMaxRequestBodySize(4)

	var payload struct {
		Name string `form:"name"`
	}

	err := c.ShouldBindForm(&payload)
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("expected ErrBodyTooLarge, got %v", err)
	}
}

func TestPostFormHonorsMaxRequestBodySize(t *testing.T) {
	t.Helper()

	body := strings.NewReader("name=abcdef")
	req := httptest.NewRequest(http.MethodPost, "/form", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	c, _ := CreateTestContextWithRequest(httptest.NewRecorder(), req)
	c.SetMaxRequestBodySize(4)

	if got := c.PostForm("name"); got != "" {
		t.Fatalf("expected empty value on over-limit form body, got %q", got)
	}
	if len(c.Errors) == 0 {
		t.Fatal("expected parse error to be recorded")
	}
	if !errors.Is(c.Errors[0], ErrBodyTooLarge) {
		t.Fatalf("expected recorded error to wrap ErrBodyTooLarge, got %v", c.Errors[0])
	}
}
