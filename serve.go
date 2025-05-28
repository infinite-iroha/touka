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
)

const defaultShutdownTimeout = 5 * time.Second // 定义默认的优雅关闭超时时间

// resolveAddress 辅助函数，处理传入的地址参数。
func resolveAddress(addr []string) string {
	switch len(addr) {
	case 0:
		return ":8080" // 默认端口
	case 1:
		return addr[0]
	default:
		panic("too many parameters for Run method") // 参数过多则报错
	}
}

// Run 启动 HTTP 服务器。
// 接受一个可选的地址参数，如果未提供则默认为 ":8080"。
func (engine *Engine) Run(addr ...string) (err error) {
	address := resolveAddress(addr) // 解析服务器地址
	log.Printf("Touka server listening on %s\n", address)
	err = http.ListenAndServe(address, engine) // 启动 HTTP 服务器
	return
}

// getShutdownTimeout 解析可选的超时参数，如果未提供或无效，则返回默认超时。
func getShutdownTimeout(timeouts []time.Duration) time.Duration {
	var timeout time.Duration
	if len(timeouts) > 0 {
		timeout = timeouts[0]
		if timeout <= 0 {
			log.Printf("Warning: Provided shutdown timeout (%v) is non-positive. Using default timeout %v.\n", timeout, defaultShutdownTimeout)
			timeout = defaultShutdownTimeout
		}
	} else {
		timeout = defaultShutdownTimeout
	}
	return timeout
}

// handleGracefulShutdown 处理一个或多个 http.Server 实例的优雅关闭。
// 它监听操作系统信号，并在指定超时时间内尝试关闭所有服务器。
func handleGracefulShutdown(servers []*http.Server, timeout time.Duration) error {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down Touka server(s)...")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var wg sync.WaitGroup
	var errs []error
	var errsMutex sync.Mutex // 保护 errs 切片

	for _, srv := range servers {
		srv := srv // capture loop variable
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Shutdown(ctx); err != nil {
				errsMutex.Lock()
				if err == context.DeadlineExceeded {
					log.Printf("Server %s shutdown timed out after %v.\n", srv.Addr, timeout)
					errs = append(errs, fmt.Errorf("server %s shutdown timed out", srv.Addr))
				} else {
					log.Printf("Server %s forced to shutdown: %v\n", srv.Addr, err)
					errs = append(errs, fmt.Errorf("server %s forced to shutdown: %w", srv.Addr, err))
				}
				errsMutex.Unlock()
			}
		}()
	}
	wg.Wait() // 等待所有服务器的关闭 Goroutine 完成

	if len(errs) > 0 {
		return errors.Join(errs...) // 返回所有收集到的错误
	}

	log.Println("Touka server(s) exited gracefully.")
	return nil
}

// RunShutdown 启动 HTTP 服务器并支持优雅关闭。
// 它监听操作系统信号 (SIGINT, SIGTERM)，并在指定超时时间内优雅地关闭服务器。
// addr: 服务器监听的地址，例如 ":8080"。
// timeouts: 可选的超时时间，如果未提供，则默认为 5 秒。
func (engine *Engine) RunShutdown(addr string, timeouts ...time.Duration) error {
	timeout := getShutdownTimeout(timeouts)

	srv := &http.Server{
		Addr:    addr,
		Handler: engine, // Engine 实现了 http.Handler 接口
	}

	// 启动服务器在单独的 Goroutine 中运行，以便主 Goroutine 可以监听信号
	go func() {
		log.Printf("Touka HTTP server listening on %s\n", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Touka HTTP server listen error: %s\n", err)
		}
	}()

	return handleGracefulShutdown([]*http.Server{srv}, timeout)
}

// RunWithTLS 启动 HTTPS 服务器并支持优雅关闭。
// 用户需自行创建并传入 *tls.Config 实例，以提供完整的 TLS 配置自由度。
// addr: 服务器监听的地址，例如 ":8443"。
// tlsConfig: 包含 TLS 证书、密钥及其他配置的 tls.Config 实例。
// timeouts: 可选的超时时间，如果未提供，则默认为 5 秒。
func (engine *Engine) RunWithTLS(addr string, tlsConfig *tls.Config, timeouts ...time.Duration) error {
	if tlsConfig == nil {
		return errors.New("tls.Config must not be nil for RunWithTLS")
	}
	timeout := getShutdownTimeout(timeouts)

	srv := &http.Server{
		Addr:      addr,
		Handler:   engine,
		TLSConfig: tlsConfig, // 使用用户传入的 tls.Config
	}

	if engine.useDefaultProtocols {
		//加入HTTP2支持
		engine.SetProtocols(&ProtocolsConfig{
			Http1:           true,
			Http2:           true, // 默认启用 HTTP/2
			Http2_Cleartext: false,
		})
	}

	go func() {
		log.Printf("Touka HTTPS server listening on %s\n", addr)
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Touka HTTPS server listen error: %s\n", err)
		}
	}()

	return handleGracefulShutdown([]*http.Server{srv}, timeout)
}

// RunWithTLSRedir 启动 HTTP 和 HTTPS 服务器，并将所有 HTTP 请求重定向到 HTTPS。
// httpAddr: HTTP 服务器监听的地址，例如 ":80"。
// httpsAddr: HTTPS 服务器监听的地址，例如 ":443"。
// tlsConfig: 包含 TLS 证书、密钥及其他配置的 tls.Config 实例，用于 HTTPS 服务器。
// timeouts: 可选的超时时间，如果未提供，则默认为 5 秒。
func (engine *Engine) RunWithTLSRedir(httpAddr, httpsAddr string, tlsConfig *tls.Config, timeouts ...time.Duration) error {
	if tlsConfig == nil {
		return errors.New("tls.Config must not be nil for RunWithTLSRedir")
	}
	timeout := getShutdownTimeout(timeouts)

	// HTTPS Server
	httpsSrv := &http.Server{
		Addr:      httpsAddr,
		Handler:   engine,
		TLSConfig: tlsConfig, // 使用用户传入的 tls.Config
	}

	if engine.useDefaultProtocols {
		//加入HTTP2支持
		engine.SetProtocols(&ProtocolsConfig{
			Http1:           true,
			Http2:           true, // 默认启用 HTTP/2
			Http2_Cleartext: false,
		})
	}

	// HTTP Server for redirection
	httpSrv := &http.Server{
		Addr: httpAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 从 r.Host 提取 hostname，例如 "localhost:8080" -> "localhost"
			hostOnly, _, err := net.SplitHostPort(r.Host)
			if err != nil { // r.Host 可能没有端口，例如 "example.com"
				hostOnly = r.Host
			}

			// 从 httpsAddr 提取目标 HTTPS 端口，例如 ":443" -> "443"
			_, targetHttpsPort, err := net.SplitHostPort(httpsAddr)
			if err != nil { // httpsAddr 必须包含一个有效的端口
				log.Fatalf("Error: Invalid HTTPS address '%s' for redirection. Must specify a port (e.g., ':443').", httpsAddr)
			}

			var redirectHost string
			if targetHttpsPort == "443" {
				redirectHost = hostOnly // 如果是默认 HTTPS 端口，则无需在 URL 中显式指定端口
			} else {
				redirectHost = net.JoinHostPort(hostOnly, targetHttpsPort) // 否则，显式指定端口
			}

			// 构建目标 HTTPS URL
			targetURL := "https://" + redirectHost + r.URL.RequestURI()
			http.Redirect(w, r, targetURL, http.StatusMovedPermanently) // 301 Permanent Redirect
		}),
	}

	// Start HTTPS server
	go func() {
		log.Printf("Touka HTTPS server listening on %s\n", httpsAddr)
		if err := httpsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed { // 同样，传入空字符串
			log.Fatalf("Touka HTTPS server listen error: %s\n", err)
		}
	}()

	// Start HTTP redirect server
	go func() {
		log.Printf("Touka HTTP redirect server listening on %s\n", httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Touka HTTP redirect server listen error: %s\n", err)
		}
	}()

	return handleGracefulShutdown([]*http.Server{httpsSrv, httpSrv}, timeout)
}
