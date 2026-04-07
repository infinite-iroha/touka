// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/fenthope/reco"
)

const defaultShutdownTimeout = 5 * time.Second

type runMode uint8

const (
	runModeHTTP runMode = iota
	runModeHTTPS
	runModeHTTPSRedirect
)

type runConfig struct {
	addr               string
	httpRedirectAddr   string
	tlsConfig          *tls.Config
	graceful           bool
	shutdownTimeout    time.Duration
	gracefulCtx        context.Context
	mode               runMode
	shutdownDefaultSet bool
	shutdownTimeoutSet bool
}

type RunOption interface {
	apply(*runConfig) error
}

type runOptionFunc func(*runConfig) error

func (f runOptionFunc) apply(cfg *runConfig) error {
	return f(cfg)
}

func defaultRunConfig() runConfig {
	return runConfig{
		addr:            ":8080",
		shutdownTimeout: defaultShutdownTimeout,
		mode:            runModeHTTP,
	}
}

func WithAddr(addr string) RunOption {
	return runOptionFunc(func(cfg *runConfig) error {
		if addr == "" {
			return errors.New("run address must not be empty")
		}
		cfg.addr = addr
		return nil
	})
}

func WithTLS(tlsConfig *tls.Config) RunOption {
	return runOptionFunc(func(cfg *runConfig) error {
		if tlsConfig == nil {
			return errors.New("tls.Config must not be nil")
		}
		cfg.tlsConfig = tlsConfig
		if cfg.mode == runModeHTTP {
			cfg.mode = runModeHTTPS
		}
		return nil
	})
}

func WithHTTPRedirect(addr string) RunOption {
	return runOptionFunc(func(cfg *runConfig) error {
		if addr == "" {
			return errors.New("http redirect address must not be empty")
		}
		cfg.httpRedirectAddr = addr
		cfg.mode = runModeHTTPSRedirect
		return nil
	})
}

func WithGracefulShutdown(timeout time.Duration) RunOption {
	return runOptionFunc(func(cfg *runConfig) error {
		cfg.graceful = true
		cfg.shutdownTimeoutSet = true
		if timeout > 0 {
			cfg.shutdownTimeout = timeout
		} else {
			cfg.shutdownTimeout = defaultShutdownTimeout
		}
		return nil
	})
}

func WithGracefulShutdownDefault() RunOption {
	return runOptionFunc(func(cfg *runConfig) error {
		cfg.graceful = true
		cfg.shutdownDefaultSet = true
		cfg.shutdownTimeout = defaultShutdownTimeout
		return nil
	})
}

func WithShutdownContext(ctx context.Context) RunOption {
	return runOptionFunc(func(cfg *runConfig) error {
		if ctx == nil {
			return errors.New("shutdown context must not be nil")
		}
		cfg.gracefulCtx = ctx
		return nil
	})
}

func serveServer(srv *http.Server, serveTLS bool) error {
	if serveTLS {
		return srv.ListenAndServeTLS("", "")
	}
	return srv.ListenAndServe()
}

func runServer(serverType string, srv *http.Server, serveTLS bool) {
	go func() {
		protocol := "http"
		if serveTLS {
			protocol = "https"
		}

		log.Printf("Touka %s server listening on %s://%s", serverType, protocol, srv.Addr)

		err := serveServer(srv, serveTLS)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Touka %s server failed: %v", serverType, err)
		}
	}()
}

func cloneTLSConfig(tlsConfig *tls.Config) *tls.Config {
	if tlsConfig == nil {
		return nil
	}
	return tlsConfig.Clone()
}

func parseHTTPSPort(addr string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("https address %q must include a port: %w", addr, err)
	}
	return port, nil
}

func applyMainServerConfig(engine *Engine, srv *http.Server, serveTLS bool) {
	if serveTLS {
		if engine.TLSServerConfigurator != nil {
			engine.TLSServerConfigurator(srv)
			return
		}
	}
	if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(srv)
	}
}

func applyRedirectServerConfig(engine *Engine, srv *http.Server) {
	applyServerProtocols(srv, engine.serverProtocols)
	if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(srv)
	}
}

func effectiveServerProtocols(engine *Engine, serveTLS bool) *http.Protocols {
	if engine == nil {
		return nil
	}
	if serveTLS && engine.useDefaultProtocols {
		protocols := &http.Protocols{}
		protocols.SetHTTP1(true)
		protocols.SetHTTP2(true)
		return protocols
	}
	return cloneServerProtocols(engine.serverProtocols)
}

func buildMainServer(engine *Engine, cfg runConfig) *http.Server {
	serveTLS := cfg.mode != runModeHTTP
	server := &http.Server{
		Addr:      cfg.addr,
		Handler:   engine,
		TLSConfig: cloneTLSConfig(cfg.tlsConfig),
	}
	if cfg.graceful {
		server.BaseContext = func(net.Listener) context.Context {
			return engine.shutdownCtx
		}
		server.RegisterOnShutdown(engine.shutdownCancel)
	}
	applyServerProtocols(server, effectiveServerProtocols(engine, serveTLS))
	applyMainServerConfig(engine, server, serveTLS)
	return server
}

func buildRedirectServer(engine *Engine, httpsAddr, httpAddr string) (*http.Server, error) {
	httpsPort, err := parseHTTPSPort(httpsAddr)
	if err != nil {
		return nil, err
	}

	redirectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}

		targetURL := "https://" + host
		if httpsPort != "443" {
			targetURL = "https://" + net.JoinHostPort(host, httpsPort)
		}
		targetURL += r.URL.RequestURI()

		http.Redirect(w, r, targetURL, http.StatusMovedPermanently)
	})

	server := &http.Server{Addr: httpAddr, Handler: redirectHandler}
	applyRedirectServerConfig(engine, server)
	return server, nil
}

func validateRunConfig(cfg runConfig) error {
	if cfg.mode == runModeHTTPSRedirect && cfg.tlsConfig == nil {
		return errors.New("WithHTTPRedirect requires WithTLS")
	}
	if cfg.mode == runModeHTTPS && cfg.tlsConfig == nil {
		return errors.New("https mode requires WithTLS")
	}
	if cfg.httpRedirectAddr != "" && cfg.mode != runModeHTTPSRedirect {
		cfg.mode = runModeHTTPSRedirect
	}
	if cfg.gracefulCtx != nil && !cfg.graceful {
		return errors.New("WithShutdownContext requires graceful shutdown")
	}
	return nil
}

func effectiveShutdownTimeout(cfg runConfig) time.Duration {
	if cfg.shutdownTimeoutSet || cfg.shutdownDefaultSet {
		if cfg.shutdownTimeout > 0 {
			return cfg.shutdownTimeout
		}
	}
	return defaultShutdownTimeout
}

func closeLoggerAsync(logger *reco.Logger) {
	if logger == nil {
		return
	}
	go func() {
		log.Println("Closing Touka logger...")
		CloseLogger(logger)
	}()
}

func shutdownServers(servers []*http.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var wg sync.WaitGroup
	errChan := make(chan error, len(servers))
	for _, srv := range servers {
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			if err := s.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errChan <- fmt.Errorf("server on %s shutdown failed: %w", s.Addr, err)
			}
		}(srv)
	}

	wg.Wait()
	close(errChan)

	var shutdownErrors []error
	for err := range errChan {
		shutdownErrors = append(shutdownErrors, err)
		log.Printf("Shutdown error: %v", err)
	}
	if len(shutdownErrors) > 0 {
		return errors.Join(shutdownErrors...)
	}
	return nil
}

func gracefulServe(servers []*http.Server, serveTLS []bool, timeout time.Duration, logger *reco.Logger, shutdownCtx context.Context) error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)

	serverStopped := make(chan error, len(servers))
	for i, srv := range servers {
		serveTLSFlag := serveTLS[i]
		go func(server *http.Server, useTLS bool) {
			serverStopped <- serveServer(server, useTLS)
		}(srv, serveTLSFlag)
	}

	select {
	case err := <-serverStopped:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			if shutdownErr := shutdownServers(servers, timeout); shutdownErr != nil {
				return errors.Join(err, shutdownErr)
			}
			return err
		}
		log.Println("Touka server stopped gracefully.")
		return nil
	case <-quit:
		log.Println("Shutting down Touka server(s) due to OS signal...")
	case <-shutdownCtx.Done():
		log.Println("Context cancelled, shutting down Touka server(s)...")
	}

	closeLoggerAsync(logger)
	if err := shutdownServers(servers, timeout); err != nil {
		return err
	}
	log.Println("Touka server(s) exited gracefully.")
	return nil
}

// Run starts the engine with the provided startup options.
//
// Default behavior with no options:
//   - HTTP only
//   - listens on :8080
//   - no graceful shutdown orchestration
//
// Add WithGracefulShutdown(...) or WithGracefulShutdownDefault() to enable
// signal-aware graceful shutdown and request-context cancellation semantics.
// Add WithTLS(...) to run HTTPS; this is independent from graceful shutdown.
func (engine *Engine) Run(opts ...RunOption) error {
	cfg := defaultRunConfig()
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt.apply(&cfg); err != nil {
			return err
		}
	}
	if cfg.httpRedirectAddr != "" {
		cfg.mode = runModeHTTPSRedirect
	} else if cfg.tlsConfig != nil {
		cfg.mode = runModeHTTPS
	}
	if err := validateRunConfig(cfg); err != nil {
		return err
	}

	serveTLS := cfg.mode != runModeHTTP

	mainServer := buildMainServer(engine, cfg)
	servers := []*http.Server{mainServer}
	serveTLSFlags := []bool{serveTLS}
	if cfg.mode == runModeHTTPSRedirect {
		redirectServer, err := buildRedirectServer(engine, cfg.addr, cfg.httpRedirectAddr)
		if err != nil {
			return err
		}
		servers = append(servers, redirectServer)
		serveTLSFlags = append(serveTLSFlags, false)
	}

	if !cfg.graceful {
		if len(servers) > 1 {
			runServer("HTTPS", servers[0], true)
			log.Printf("Starting Touka HTTP Redirect server on %s", servers[1].Addr)
			return serveServer(servers[1], false)
		}

		protocolLabel := "HTTP"
		if serveTLS {
			protocolLabel = "HTTPS"
		}
		log.Printf("Starting Touka %s server on %s", protocolLabel, cfg.addr)
		return serveServer(mainServer, serveTLS)
	}

	shutdownCtx := context.Background()
	if cfg.gracefulCtx != nil {
		shutdownCtx = cfg.gracefulCtx
	}
	return gracefulServe(servers, serveTLSFlags, effectiveShutdownTimeout(cfg), engine.LogReco, shutdownCtx)
}
