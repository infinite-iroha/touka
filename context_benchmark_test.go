package touka

import (
	"net/http"
	"testing"
)

func TestContextResetKeepsKeysNilUntilSet(t *testing.T) {
	c, _ := CreateTestContext(nil)
	if c.Keys != nil {
		t.Fatalf("expected fresh test context Keys to be nil before first Set")
	}

	c.Set("answer", 42)
	if c.Keys == nil {
		t.Fatalf("expected Set to allocate Keys map")
	}
	if value, exists := c.Get("answer"); !exists || value != 42 {
		t.Fatalf("expected stored value to round-trip, got %v, %t", value, exists)
	}

	req, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	c.reset(c.Writer, req)

	if c.Keys != nil {
		t.Fatalf("expected reset to clear Keys without allocating a new map")
	}
	if value, exists := c.Get("answer"); exists || value != nil {
		t.Fatalf("expected cleared keys after reset, got %v, %t", value, exists)
	}

	ctxValue := c.Value("missing")
	if ctxValue != nil {
		t.Fatalf("expected nil value for missing context key after reset, got %v", ctxValue)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected MustGet to panic for missing key after reset")
		}
	}()
	_ = c.MustGet("answer")
}

func BenchmarkContextReset(b *testing.B) {
	b.Run("NoKeysUse", func(b *testing.B) {
		c, _ := CreateTestContext(nil)
		req, err := http.NewRequest(http.MethodGet, "/", nil)
		if err != nil {
			b.Fatalf("failed to build request: %v", err)
		}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			c.reset(c.Writer, req)
		}
	})

	b.Run("WithKeysUse", func(b *testing.B) {
		c, _ := CreateTestContext(nil)
		req, err := http.NewRequest(http.MethodGet, "/", nil)
		if err != nil {
			b.Fatalf("failed to build request: %v", err)
		}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			c.reset(c.Writer, req)
			c.Set("request-id", i)
		}
	})
}
