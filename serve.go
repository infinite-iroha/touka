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

// defaultShutdownTimeout 定义了在强制关闭前等待优雅关闭的最长时间
const defaultShutdownTimeout = 5 * time.Second

// --- 内部辅助函数 ---

// resolveAddress 解析传入的地址参数,如果没有则返回默认的 ":8080"
func resolveAddress(addr []string) string {
	switch len(addr) {
	case 0:
		return ":8080"
	case 1:
		return addr[0]
	default:
		panic("too many parameters provided for server address")
	}
}

// getShutdownTimeout 解析可选的超时参数,如果无效或未提供则返回默认值
func getShutdownTimeout(timeouts []time.Duration) time.Duration {
	if len(timeouts) > 0 && timeouts[0] > 0 {
		return timeouts[0]
	}
	return defaultShutdownTimeout
}

// runServer 是一个内部辅助函数,负责在一个新的 goroutine 中启动一个 http.Server,
// 并处理其启动失败的致命错误
// serverType 用于在日志中标识服务器类型 (例如 "HTTP", "HTTPS")
func runServer(serverType string, srv *http.Server) {
	go func() {
		var err error
		protocol := "http"
		if srv.TLSConfig != nil {
			protocol = "https"
		}

		log.Printf("Touka %s server listening on %s://%s", serverType, protocol, srv.Addr)

		if srv.TLSConfig != nil {
			// 对于 HTTPS 服务器,如果 srv.TLSConfig.Certificates 已配置,
			// ListenAndServeTLS 的前两个参数可以为空字符串
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}

		// 如果服务器停止不是因为被优雅关闭 (http.ErrServerClosed),
		// 则认为是一个严重错误,并终止程序
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Touka %s server failed: %v", serverType, err)
		}
	}()
}

// handleGracefulShutdown 监听系统信号 (SIGINT, SIGTERM) 并优雅地关闭所有提供的服务器
// 这是所有支持优雅关闭的 RunXXX 方法的最终归宿
func handleGracefulShutdown(servers []*http.Server, timeout time.Duration, logger *reco.Logger) error {
	// 创建一个 channel 来接收操作系统信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) // 监听中断和终止信号
	<-quit                                               // 阻塞,直到接收到上述信号之一
	log.Println("Shutting down Touka server(s)...")

	// 关闭日志记录器
	if logger != nil {
		go func() {
			log.Println("Closing Touka logger...")
			CloseLogger(logger)
		}()
	}

	// 创建一个带超时的上下文,用于 Shutdown
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var wg sync.WaitGroup
	errChan := make(chan error, len(servers)) // 用于收集关闭错误的 channel

	// 并发地关闭所有服务器
	for _, srv := range servers {
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			if err := s.Shutdown(ctx); err != nil {
				// 将错误发送到 channel
				errChan <- fmt.Errorf("server on %s shutdown failed: %w", s.Addr, err)
			}
		}(srv)
	}

	wg.Wait()      // 等待所有服务器的关闭 goroutine 完成
	close(errChan) // 关闭 channel,以便可以安全地遍历它

	// 收集所有关闭过程中发生的错误
	var shutdownErrors []error
	for err := range errChan {
		shutdownErrors = append(shutdownErrors, err)
		log.Printf("Shutdown error: %v", err)
	}

	if len(shutdownErrors) > 0 {
		return errors.Join(shutdownErrors...) // Go 1.20+ 的 errors.Join,用于合并多个错误
	}
	log.Println("Touka server(s) exited gracefully.")
	return nil
}

func handleGracefulShutdownWithContext(servers []*http.Server, ctx context.Context, timeout time.Duration, logger *reco.Logger) error {
	// 创建一个 channel 来接收操作系统信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) // 监听中断和终止信号

	// 启动服务器
	serverStopped := make(chan error, 1)
	for _, srv := range servers {
		go func(s *http.Server) {
			serverStopped <- s.ListenAndServe()
		}(srv)
	}

	select {
	case <-ctx.Done():
		// Context 被取消 (例如,通过外部取消函数)
		log.Println("Context cancelled, shutting down Touka server(s)...")
	case err := <-serverStopped:
		// 服务器自身停止 (例如,端口被占用,或 ListenAndServe 返回错误)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("Touka HTTP server failed: %w", err)
		}
		log.Println("Touka HTTP server stopped gracefully.")
		return nil // 服务器已自行优雅关闭,无需进一步处理
	case <-quit:
		// 接收到操作系统信号
		log.Println("Shutting down Touka server(s) due to OS signal...")
	}

	// 关闭日志记录器
	if logger != nil {
		go func() {
			log.Println("Closing Touka logger...")
			CloseLogger(logger)
		}()
	}

	// 创建一个带超时的上下文,用于 Shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var wg sync.WaitGroup
	errChan := make(chan error, len(servers)) // 用于收集关闭错误的 channel

	// 并发地关闭所有服务器
	for _, srv := range servers {
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			if err := s.Shutdown(shutdownCtx); err != nil {
				// 将错误发送到 channel
				errChan <- fmt.Errorf("server on %s shutdown failed: %w", s.Addr, err)
			}
		}(srv)
	}

	wg.Wait()
	close(errChan) // 关闭 channel,以便可以安全地遍历它

	// 收集所有关闭过程中发生的错误
	var shutdownErrors []error
	for err := range errChan {
		shutdownErrors = append(shutdownErrors, err)
		log.Printf("Shutdown error: %v", err)
	}

	if len(shutdownErrors) > 0 {
		return errors.Join(shutdownErrors...) // Go 1.20+ 的 errors.Join,用于合并多个错误
	}
	log.Println("Touka server(s) exited gracefully.")
	return nil
}

// --- 公共 Run 方法 ---

// Run 启动一个不支持优雅关闭的 HTTP 服务器
// 这是一个阻塞调用,主要用于简单的场景或快速测试
// 建议在生产环境中使用 RunShutdown 或其他支持优雅关闭的方法
func (engine *Engine) Run(addr ...string) error {
	address := resolveAddress(addr)
	srv := &http.Server{Addr: address, Handler: engine}

	// 即使是不支持优雅关闭的 Run,也应用默认和用户配置,以保持行为一致性
	//engine.applyDefaultServerConfig(srv)
	if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(srv)
	}
	log.Printf("Starting Touka HTTP server on %s (no graceful shutdown)", address)
	return srv.ListenAndServe()
}

// RunShutdown 启动一个支持优雅关闭的 HTTP 服务器
func (engine *Engine) RunShutdown(addr string, timeouts ...time.Duration) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: engine,
	}

	// 应用框架的默认配置和用户提供的自定义配置
	//engine.applyDefaultServerConfig(srv)
	if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(srv)
	}

	runServer("HTTP", srv)
	return handleGracefulShutdown([]*http.Server{srv}, getShutdownTimeout(timeouts), engine.LogReco)
}

// RunShutdown 启动一个支持优雅关闭的 HTTP 服务器
func (engine *Engine) RunShutdownWithContext(addr string, ctx context.Context, timeouts ...time.Duration) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: engine,
	}

	// 应用框架的默认配置和用户提供的自定义配置
	//engine.applyDefaultServerConfig(srv)
	if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(srv)
	}

	return handleGracefulShutdownWithContext([]*http.Server{srv}, ctx, getShutdownTimeout(timeouts), engine.LogReco)
}

// RunTLS 启动一个支持优雅关闭的 HTTPS 服务器
func (engine *Engine) RunTLS(addr string, tlsConfig *tls.Config, timeouts ...time.Duration) error {
	if tlsConfig == nil {
		return errors.New("tls.Config must not be nil for RunTLS")
	}

	// 配置 HTTP/2 支持 (如果使用默认配置)
	if engine.useDefaultProtocols {
		engine.SetProtocols(&ProtocolsConfig{
			Http1: true,
			Http2: true, // 默认在 TLS 上启用 HTTP/2
		})
	}

	srv := &http.Server{
		Addr:      addr,
		Handler:   engine,
		TLSConfig: tlsConfig,
	}

	// 应用框架的默认配置和用户提供的自定义配置
	// 优先使用 TLSServerConfigurator,如果未设置,则回退到通用的 ServerConfigurator
	//engine.applyDefaultServerConfig(srv)
	if engine.TLSServerConfigurator != nil {
		engine.TLSServerConfigurator(srv)
	} else if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(srv)
	}

	runServer("HTTPS", srv)
	return handleGracefulShutdown([]*http.Server{srv}, getShutdownTimeout(timeouts), engine.LogReco)
}

// RunWithTLS 是 RunTLS 的别名,为了保持向后兼容性或更直观的命名
func (engine *Engine) RunWithTLS(addr string, tlsConfig *tls.Config, timeouts ...time.Duration) error {
	return engine.RunTLS(addr, tlsConfig, timeouts...)
}

// RunTLSRedir 启动 HTTP 重定向服务器和 HTTPS 应用服务器,两者都支持优雅关闭
func (engine *Engine) RunTLSRedir(httpAddr, httpsAddr string, tlsConfig *tls.Config, timeouts ...time.Duration) error {
	if tlsConfig == nil {
		return errors.New("tls.Config must not be nil for RunTLSRedir")
	}

	// --- HTTPS 服务器 ---
	if engine.useDefaultProtocols {
		engine.SetProtocols(&ProtocolsConfig{Http1: true, Http2: true})
	}
	httpsSrv := &http.Server{
		Addr:      httpsAddr,
		Handler:   engine,
		TLSConfig: tlsConfig,
	}
	//engine.applyDefaultServerConfig(httpsSrv)
	if engine.TLSServerConfigurator != nil {
		engine.TLSServerConfigurator(httpsSrv)
	} else if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(httpsSrv)
	}

	// --- HTTP 重定向服务器 ---
	redirectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}

		_, httpsPort, err := net.SplitHostPort(httpsAddr)
		if err != nil {
			// 如果 httpsAddr 没有端口,这是一个配置错误

			log.Fatalf("Invalid HTTPS address for redirection '%s': must include a port.", httpsAddr)
		}

		targetURL := "https://" + host
		// 只有在非标准 HTTPS 端口 (443) 时才附加端口号
		if httpsPort != "443" {
			targetURL = "https://" + net.JoinHostPort(host, httpsPort)
		}
		targetURL += r.URL.RequestURI()

		http.Redirect(w, r, targetURL, http.StatusMovedPermanently)
	})
	httpSrv := &http.Server{
		Addr:    httpAddr,
		Handler: redirectHandler,
	}
	//engine.applyDefaultServerConfig(httpSrv)
	if engine.ServerConfigurator != nil {
		engine.ServerConfigurator(httpSrv)
	}

	// --- 启动服务器和优雅关闭 ---
	runServer("HTTPS", httpsSrv)
	runServer("HTTP Redirect", httpSrv)
	return handleGracefulShutdown([]*http.Server{httpsSrv, httpSrv}, getShutdownTimeout(timeouts), engine.LogReco)
}

// RunWithTLSRedir 是 RunTLSRedir 的别名,为了保持向后兼容性
func (engine *Engine) RunWithTLSRedir(httpAddr, httpsAddr string, tlsConfig *tls.Config, timeouts ...time.Duration) error {
	return engine.RunTLSRedir(httpAddr, httpsAddr, tlsConfig, timeouts...)
}
