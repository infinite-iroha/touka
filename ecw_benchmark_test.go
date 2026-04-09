package touka

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestErrorCapturingResponseWriterResetClearsHeaderSnapshot(t *testing.T) {
	c, _ := CreateTestContext(nil)
	ecw := AcquireErrorCapturingResponseWriter(c)
	defer ReleaseErrorCapturingResponseWriter(ecw)

	ecw.capturedErrorSignal = true
	ecw.Header().Set("Content-Type", "text/plain")
	ecw.Header().Add("X-Test", "one")

	req, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	ecw.reset(httptest.NewRecorder(), req, c, c.engine.errorHandle.handler)

	if len(ecw.headerSnapshot) != 0 {
		t.Fatalf("expected header snapshot to be empty after reset, got %#v", ecw.headerSnapshot)
	}
}

func BenchmarkErrorCapturingResponseWriterReset(b *testing.B) {
	c, _ := CreateTestContext(nil)
	ecw := AcquireErrorCapturingResponseWriter(c)
	defer ReleaseErrorCapturingResponseWriter(ecw)

	rawWriter := httptest.NewRecorder()
	req, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		b.Fatalf("failed to build request: %v", err)
	}

	keys := make([]string, 16)
	for i := range keys {
		keys[i] = http.CanonicalHeaderKey("X-Test-" + string(rune('A'+i)))
	}
	values := []string{"one", "two", "three"}
	for _, key := range keys {
		ecw.headerSnapshot[key] = values
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		ecw.reset(rawWriter, req, c, c.engine.errorHandle.handler)
		for _, key := range keys {
			ecw.headerSnapshot[key] = values
		}
	}
}
