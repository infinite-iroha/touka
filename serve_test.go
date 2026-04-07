package touka

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func generateSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create self-signed cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse self-signed cert: %v", err)
	}
	return cert
}

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

func TestRunRejectsRedirectWithoutTLS(t *testing.T) {
	engine := New()
	err := engine.Run(WithHTTPRedirect(":80"))
	if err == nil {
		t.Fatal("expected redirect mode without TLS to fail")
	}
}

func TestRunRejectsRedirectHostHeadersWithoutExplicitUseHeaderHostTrue(t *testing.T) {
	engine := New()
	err := engine.Run(
		WithAddr(":443"),
		WithTLS(&tls.Config{}),
		WithHTTPRedirect(":80", WithRedirectHostHeaders([]string{"X-Forwarded-Host"})),
	)
	if err == nil {
		t.Fatal("expected redirect host headers without explicit WithUseHeaderHost(true) to fail")
	}
}

func TestWithGracefulShutdownDefaultUsesDefaultTimeout(t *testing.T) {
	cfg := defaultRunConfig()
	if err := WithGracefulShutdownDefault().apply(&cfg); err != nil {
		t.Fatalf("apply graceful default option: %v", err)
	}
	if !cfg.graceful {
		t.Fatal("expected graceful shutdown to be enabled")
	}
	if cfg.shutdownTimeout != defaultShutdownTimeout {
		t.Fatalf("expected default shutdown timeout %v, got %v", defaultShutdownTimeout, cfg.shutdownTimeout)
	}
}

func TestWithTLSDoesNotRequireGracefulShutdown(t *testing.T) {
	cfg := defaultRunConfig()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if err := WithTLS(tlsConfig).apply(&cfg); err != nil {
		t.Fatalf("apply TLS option: %v", err)
	}
	if cfg.mode != runModeHTTPS {
		t.Fatalf("expected HTTPS mode, got %v", cfg.mode)
	}
	if cfg.graceful {
		t.Fatal("expected TLS option to remain independent from graceful shutdown")
	}
	if cfg.tlsConfig != tlsConfig {
		t.Fatal("expected TLS config to be preserved in run config")
	}
}

func TestBuildRedirectServerRejectsHTTPSAddrWithoutPort(t *testing.T) {
	engine := New()
	if _, err := buildRedirectServer(engine, runConfig{addr: "example.com", httpRedirectAddr: ":80"}); err == nil {
		t.Fatal("expected redirect server builder to reject https address without port")
	}
}

func TestValidateRunConfigRejectsShutdownContextWithoutGraceful(t *testing.T) {
	cfg := defaultRunConfig()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := WithShutdownContext(ctx).apply(&cfg); err != nil {
		t.Fatalf("apply shutdown context option: %v", err)
	}
	if err := validateRunConfig(cfg); err == nil {
		t.Fatal("expected shutdown context without graceful shutdown to fail validation")
	}
}

func TestValidateRunConfigDoesNotMutateMode(t *testing.T) {
	cfg := defaultRunConfig()
	cfg.httpRedirectAddr = ":80"
	if err := validateRunConfig(cfg); err != nil {
		t.Fatalf("validate run config: %v", err)
	}
	if cfg.mode != runModeHTTP {
		t.Fatalf("expected validateRunConfig to leave mode unchanged, got %v", cfg.mode)
	}
}

func TestValidateRunConfigRejectsConfiguredHostModeWithoutRedirectHost(t *testing.T) {
	cfg := defaultRunConfig()
	cfg.mode = runModeHTTPSRedirect
	cfg.tlsConfig = &tls.Config{}
	cfg.useHeaderHost = false
	cfg.useHeaderHostSet = true
	if err := validateRunConfig(cfg); err == nil {
		t.Fatal("expected configured host mode without redirect host to fail validation")
	}
}

func TestValidateRunConfigRejectsRedirectHostWhenHeaderModeEnabled(t *testing.T) {
	cfg := defaultRunConfig()
	cfg.mode = runModeHTTPSRedirect
	cfg.tlsConfig = &tls.Config{}
	cfg.useHeaderHost = true
	cfg.useHeaderHostSet = true
	cfg.redirectHost = "configured.example"
	if err := validateRunConfig(cfg); err == nil {
		t.Fatal("expected redirect host to be rejected when header host mode is enabled")
	}
}

func TestBuildMainServerGracefulSetsBaseContextAndShutdownHook(t *testing.T) {
	engine := New()
	server := buildMainServer(engine, runConfig{addr: ":8080", graceful: true, mode: runModeHTTP})
	if server.BaseContext == nil {
		t.Fatal("expected graceful main server to set BaseContext")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for base context check: %v", err)
	}
	defer listener.Close()
	if got := server.BaseContext(listener); got != engine.shutdownCtx {
		t.Fatal("expected graceful main server to use engine shutdown context")
	}
}

func TestBuildMainServerTLSConfiguratorPrecedence(t *testing.T) {
	engine := New()
	serverConfigured := false
	tlsConfigured := false
	engine.SetServerConfigurator(func(s *http.Server) {
		serverConfigured = true
		s.ReadTimeout = time.Second
	})
	engine.SetTLSServerConfigurator(func(s *http.Server) {
		tlsConfigured = true
		s.IdleTimeout = time.Second
	})

	server := buildMainServer(engine, runConfig{addr: ":443", mode: runModeHTTPS, tlsConfig: &tls.Config{}})
	if !tlsConfigured {
		t.Fatal("expected TLS configurator to run for HTTPS main server")
	}
	if serverConfigured {
		t.Fatal("expected generic server configurator to be skipped when TLS configurator is set")
	}
	if server.IdleTimeout != time.Second {
		t.Fatal("expected TLS configurator changes to be applied to HTTPS main server")
	}
}

func TestBuildRedirectServerUsesGenericConfigurator(t *testing.T) {
	engine := New()
	configured := false
	engine.SetServerConfigurator(func(s *http.Server) {
		configured = true
		s.ReadTimeout = time.Second
	})

	server, err := buildRedirectServer(engine, runConfig{addr: ":443", httpRedirectAddr: ":80"})
	if err != nil {
		t.Fatalf("build redirect server: %v", err)
	}
	if !configured {
		t.Fatal("expected redirect server to use generic server configurator")
	}
	if server.ReadTimeout != time.Second {
		t.Fatal("expected redirect server configurator changes to be applied")
	}
}

func TestTLSRunDoesNotMutateDefaultHTTPProtocols(t *testing.T) {
	engine := New()
	httpsServer := buildMainServer(engine, runConfig{addr: ":443", mode: runModeHTTPS, tlsConfig: &tls.Config{}})
	if !httpsServer.Protocols.HTTP2() {
		t.Fatal("expected HTTPS server to enable HTTP/2 under default protocol settings")
	}

	httpServer := buildMainServer(engine, defaultRunConfig())
	if httpServer.Protocols.HTTP2() {
		t.Fatal("expected later plain HTTP server to keep default HTTP/2 disabled")
	}
}

func TestBuildRedirectServerRedirectsWithoutGracefulMode(t *testing.T) {
	engine := New()
	server, err := buildRedirectServer(engine, runConfig{addr: ":443", httpRedirectAddr: ":80"})
	if err != nil {
		t.Fatalf("build redirect server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/plain/path?q=1", nil)
	req.Host = "example.com:80"
	rr := httptest.NewRecorder()
	server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("expected redirect status %d, got %d", http.StatusMovedPermanently, rr.Code)
	}
	if location := rr.Header().Get("Location"); location != "https://example.com/plain/path?q=1" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

func TestBuildRedirectServerUsesConfiguredHeadersInOrder(t *testing.T) {
	engine := New()
	server, err := buildRedirectServer(engine, runConfig{
		addr:                ":443",
		httpRedirectAddr:    ":80",
		useHeaderHost:       true,
		useHeaderHostSet:    true,
		redirectHostHeaders: []string{"X-First-Host", "X-Forwarded-Host"},
	})
	if err != nil {
		t.Fatalf("build redirect server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/plain/path?q=1", nil)
	req.Host = "example.com:80"
	req.Header.Set("X-Forwarded-Host", "forwarded.example")
	req.Header.Set("X-First-Host", "first.example")
	rr := httptest.NewRecorder()
	server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("expected redirect status %d, got %d", http.StatusMovedPermanently, rr.Code)
	}
	if location := rr.Header().Get("Location"); location != "https://first.example/plain/path?q=1" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

func TestBuildRedirectServerReturns426WhenConfiguredHeadersMiss(t *testing.T) {
	engine := New()
	server, err := buildRedirectServer(engine, runConfig{
		addr:                ":443",
		httpRedirectAddr:    ":80",
		useHeaderHost:       true,
		useHeaderHostSet:    true,
		redirectHostHeaders: []string{"X-Forwarded-Host"},
	})
	if err != nil {
		t.Fatalf("build redirect server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/plain/path?q=1", nil)
	req.Host = "example.com:80"
	rr := httptest.NewRecorder()
	server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUpgradeRequired {
		t.Fatalf("expected status %d when configured redirect headers miss, got %d", http.StatusUpgradeRequired, rr.Code)
	}
}

func TestBuildRedirectServerUsesConfiguredRedirectHostWhenHeaderModeDisabled(t *testing.T) {
	engine := New()
	server, err := buildRedirectServer(engine, runConfig{
		addr:             ":443",
		httpRedirectAddr: ":80",
		useHeaderHost:    false,
		useHeaderHostSet: true,
		redirectHost:     "configured.example",
	})
	if err != nil {
		t.Fatalf("build redirect server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/plain/path?q=1", nil)
	req.Host = "example.com:80"
	req.Header.Set("X-Forwarded-Host", "forwarded.example")
	rr := httptest.NewRecorder()
	server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusMovedPermanently {
		t.Fatalf("expected redirect status %d, got %d", http.StatusMovedPermanently, rr.Code)
	}
	if location := rr.Header().Get("Location"); location != "https://configured.example/plain/path?q=1" {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

func TestGracefulServeShutsDownSiblingServersOnStartupFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on occupied addr: %v", err)
	}
	occupiedAddr := occupied.Addr().String()
	defer occupied.Close()

	redirectListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for redirect addr: %v", err)
	}
	redirectAddr := redirectListener.Addr().String()
	if err := redirectListener.Close(); err != nil {
		t.Fatalf("close redirect addr probe: %v", err)
	}

	engine := New()
	redirectServer, err := buildRedirectServer(engine, runConfig{addr: ":443", httpRedirectAddr: redirectAddr})
	if err != nil {
		t.Fatalf("build redirect server: %v", err)
	}
	mainServer := &http.Server{Addr: occupiedAddr, Handler: engine}

	err = gracefulServe([]*http.Server{mainServer, redirectServer}, []bool{false, false}, 200*time.Millisecond, nil, context.Background())
	if err == nil {
		t.Fatal("expected gracefulServe to fail when one server cannot bind")
	}
	if !strings.Contains(err.Error(), occupiedAddr) {
		t.Fatalf("expected startup failure to mention occupied address %q, got %v", occupiedAddr, err)
	}

	conn, dialErr := net.DialTimeout("tcp", redirectAddr, 200*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		t.Fatalf("expected sibling redirect server to be shut down after startup failure, but %s is still accepting connections", redirectAddr)
	}
	if !strings.Contains(dialErr.Error(), "refused") && !strings.Contains(dialErr.Error(), "reset") {
		t.Fatalf("unexpected dial result after shutdown, got %v", dialErr)
	}
}

func TestRunNonGracefulRedirectReturnsStartupError(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on occupied addr: %v", err)
	}
	occupiedAddr := occupied.Addr().String()
	defer occupied.Close()

	redirectListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for redirect addr: %v", err)
	}
	redirectAddr := redirectListener.Addr().String()
	if err := redirectListener.Close(); err != nil {
		t.Fatalf("close redirect addr probe: %v", err)
	}

	engine := New()
	err = engine.Run(
		WithAddr(occupiedAddr),
		WithTLS(&tls.Config{}),
		WithHTTPRedirect(redirectAddr),
	)
	if err == nil {
		t.Fatal("expected non-graceful TLS redirect startup to return bind error")
	}
	if !strings.Contains(err.Error(), occupiedAddr) {
		t.Fatalf("expected startup error to mention occupied address %q, got %v", occupiedAddr, err)
	}
}
