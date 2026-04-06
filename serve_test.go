package touka

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeServerHTTPModeIgnoresTLSConfig(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on ephemeral port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close temporary listener: %v", err)
	}

	srv := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
		// RunShutdown uses the HTTP startup path and must not let a shared
		// ServerConfigurator accidentally turn it into HTTPS.
		TLSConfig: &tls.Config{},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- serveServer(srv, false)
	}()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	var resp *http.Response
	requestURL := "http://" + addr

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = client.Get(requestURL)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		select {
		case serveErr := <-errCh:
			t.Fatalf("expected HTTP server to accept plain HTTP with TLSConfig set: request error=%v, serve error=%v", err, serveErr)
		default:
			t.Fatalf("expected HTTP server to accept plain HTTP with TLSConfig set: %v", err)
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: got %d want %d", resp.StatusCode, http.StatusOK)
	}
	if string(body) != "ok" {
		t.Fatalf("unexpected body: got %q want %q", string(body), "ok")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown server: %v", err)
	}

	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("serveServer should stop with ErrServerClosed after shutdown, got %v", err)
	}
}
