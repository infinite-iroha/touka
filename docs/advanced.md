# 高级特性与优化

本章节涵盖了 Touka 的一些深层特性以及在生产环境中的最佳实践。

## 性能优化

### 1. Context 池化

Touka 使用 `sync.Pool` 来重用 `touka.Context` 对象。这极大减少了每个请求产生的内存分配和 GC 压力。
- **代价**: 您必须在处理器返回后立即停止对该 `Context` 指针的任何引用。
- **解决方案**: 如果需要在后台 Goroutine 中使用请求数据，请预先提取所需数据（如 `c.Query` 的值），或者深拷贝该对象（不推荐）。

### 2. 预分配参数切片

在路由匹配过程中，Touka 会预分配路径参数切片，并根据路由深度进行缓存，从而在路由查找时实现几乎零分配。

## 服务器配置

### 服务器配置器 (ServerConfigurator)

Touka 允许您在服务器启动前对底层 `*http.Server` 进行自定义配置：

```go
r := touka.New()

// 配置 HTTP 服务器
r.SetServerConfigurator(func(server *http.Server) {
    server.ReadTimeout = 30 * time.Second
    server.WriteTimeout = 30 * time.Second
    server.IdleTimeout = 120 * time.Second
    server.MaxHeaderBytes = 1 << 20 // 1MB
})

// 专门配置 HTTPS 服务器（优先级高于 ServerConfigurator）
r.SetTLSServerConfigurator(func(server *http.Server) {
    server.ReadTimeout = 30 * time.Second
    server.WriteTimeout = 30 * time.Second
    // HTTPS 特定配置...
})
```

### 协议配置

Touka 支持配置 HTTP/1.1、HTTP/2 和 H2C（HTTP/2 Cleartext）：

```go
// 使用默认协议配置
// 普通 HTTP 启动时默认为 HTTP/1.1；若使用 WithTLS(...) 且未手动覆盖协议集，
// HTTPS 服务器会默认启用 HTTP/1.1 与 HTTP/2。
r.SetDefaultProtocols()

// 自定义协议配置
r.SetProtocols(&touka.ProtocolsConfig{
    Http1:           true,  // 启用 HTTP/1.1
    Http2:           true,  // 启用 HTTP/2（需要 TLS）
    Http2_Cleartext: true,  // 启用 H2C（无需 TLS 的 HTTP/2）
})
```

### 启动方式

Touka 统一通过 `Run(opts...)` 启动服务器：

```go
// 1. 简单启动（无优雅停机）
r.Run(touka.WithAddr(":8080"))

// 2. 带优雅停机的启动
r.Run(touka.WithAddr(":8080"), touka.WithGracefulShutdown(10*time.Second))

// 3. 带上下文的优雅停机
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
r.Run(
    touka.WithAddr(":8080"),
    touka.WithGracefulShutdown(10*time.Second),
    touka.WithShutdownContext(ctx),
)

// 4. HTTPS 启动
tlsConfig := &tls.Config{
    MinVersion: tls.VersionTLS12,
    // 其他 TLS 配置...
}
// WithTLS(...) 与优雅关闭相互独立；这里演示 HTTPS + 默认优雅关闭超时。
r.Run(
    touka.WithAddr(":443"),
    touka.WithTLS(tlsConfig),
    touka.WithGracefulShutdownDefault(),
)

// 5. HTTPS + HTTP 重定向
// WithHTTPRedirect(...) 需要与 WithTLS(...) 配合使用。
r.Run(
    touka.WithAddr(":443"),
    touka.WithTLS(tlsConfig),
    touka.WithHTTPRedirect(":80"),
    touka.WithGracefulShutdown(10*time.Second),
)
```

## 优雅停机 (Graceful Shutdown)

在部署新版本时，我们希望服务器停止接收新请求，但能处理完当前正在进行的请求。启用优雅关闭后，Touka 会监听 `SIGINT`/`SIGTERM`，并在关闭时取消活动请求的上下文。

```go
r := touka.Default()
// ... 注册路由 ...

// 监听 SIGINT 和 SIGTERM 信号
// 如果在 10 秒内未处理完，则强制关闭
if err := r.Run(touka.WithAddr(":8080"), touka.WithGracefulShutdown(10*time.Second)); err != nil {
    log.Fatal("服务器退出异常:", err)
}
```

### SSE 长连接的优雅关闭

对于 SSE 等长连接场景，Touka 会自动将引擎的关闭信号注入到请求的 Context 中：

```go
r.GET("/events", func(c *touka.Context) {
    c.EventStream(func(w io.Writer) bool {
        select {
        case <-c.Request.Context().Done():
            // 收到关闭信号，优雅退出
            return false
        case <-time.After(1 * time.Second):
            // 发送数据
            event := touka.Event{Data: "tick"}
            event.Render(w)
            return true
        }
    })
})
```

## 路由行为配置

```go
r := touka.New()

// 是否自动重定向尾部斜杠（默认 true）
// /foo/ -> /foo 或 /foo -> /foo/
r.SetRedirectTrailingSlash(true)

// 是否自动修复路径大小写（默认 true）
// /FOO -> /foo
r.SetRedirectFixedPath(true)

// 是否处理 405 Method Not Allowed（默认 true）
// 当路径匹配但方法不匹配时返回 405 而非 404
r.SetHandleMethodNotAllowed(true)
```

### 自定义 404 处理

```go
// 单个处理器
r.NoRoute(func(c *touka.Context) {
    c.JSON(http.StatusNotFound, touka.H{
        "error": "Page not found",
        "path":  c.Request.URL.Path,
    })
})

// 处理器链（可以在 404 前执行额外中间件）
r.NoRoutes(
    LogNotFoundMiddleware(),
    func(c *touka.Context) {
        c.JSON(http.StatusNotFound, touka.H{"error": "Not found"})
    },
)
```

### 未匹配路径作为静态文件服务

```go
// 当没有路由匹配时，尝试从文件系统中查找文件
// 非常适合单页应用（SPA）部署
r.SetUnMatchFS(http.Dir("./frontend/dist"))

// 也可以添加额外的中间件
r.SetUnMatchFS(http.Dir("./frontend/dist"), AuthMiddleware())
```

## IP 地址解析配置

在反向代理环境中，正确配置 IP 解析非常重要：

```go
r := touka.New()

// 是否信任代理头部获取客户端 IP（默认 true）
r.SetForwardByClientIP(true)

// 设置用于获取客户端 IP 的头部列表（按优先级排序）
r.SetRemoteIPHeaders([]string{
    "X-Forwarded-For",
    "X-Real-IP",
    "CF-Connecting-IP", // Cloudflare
})
```

如果您同时使用 Touka 的 `ReverseProxy` 把请求继续转发给其他后端，请再参考 `docs/reverse-proxy.md` 中关于 `Forwarded`、`X-Forwarded-*` 与 `Via` 的说明。前者解决“当前请求的客户端 IP 如何被 Touka 正确解析”，后者解决“代理后的请求如何把链路信息继续传给下一跳”。

## 请求体大小限制

为了防止恶意的大数据包攻击（如慢速 HTTP 攻击或内存溢出），Touka 内置了请求体大小限制机制。

### 全局限制

```go
// 设置全局最大请求体大小（例如 10MB）
r.SetGlobalMaxRequestBodySize(10 << 20)
```

### 单个请求限制

```go
r.POST("/upload", func(c *touka.Context) {
    // 为特定请求设置限制（覆盖全局设置）
    c.SetMaxRequestBodySize(100 << 20) // 100MB

    body, err := c.GetReqBodyFull()
    if err != nil {
        // 如果超过限制，会返回 ErrBodyTooLarge
        if errors.Is(err, touka.ErrBodyTooLarge) {
            c.ErrorUseHandle(http.StatusRequestEntityTooLarge, err)
            return
        }
        c.ErrorUseHandle(http.StatusBadRequest, err)
        return
    }
    // 处理 body...
})
```

## 与标准库集成

Touka 遵循 `net/http` 哲学。您可以方便地使用现有的标准库组件。

### 适配 `http.HandlerFunc`

```go
r.GET("/pprof/*any", touka.AdapterStdFunc(pprof.Index))
```

### 适配 `http.Handler`

```go
// 适配 http.FileServer
fileServer := http.FileServer(http.Dir("./static"))
r.GET("/static/*filepath", touka.AdapterStdHandle(http.StripPrefix("/static", fileServer)))
```

### 手动注入

由于 `Engine` 实现了 `http.Handler` 接口，您可以将其挂载到任何地方。

```go
s := &http.Server{
    Addr:           ":8080",
    Handler:        r, // Engine 实例
    ReadTimeout:    10 * time.Second,
    WriteTimeout:   10 * time.Second,
    MaxHeaderBytes: 1 << 20,
}
s.ListenAndServe()
```

## 自定义日志集成

Touka 默认集成了 `reco` 日志库。您可以自定义其输出行为。

```go
logConfig := reco.Config{
    Level:      reco.LevelInfo,
    Mode:       reco.ModeText, // 或 reco.ModeJSON
    Output:     os.Stdout,
    Async:      true, // 异步写入提高性能
    TimeFormat: time.RFC3339,
}
r.SetLoggerCfg(logConfig)

// 或直接传入日志实例
logger, _ := reco.New(logConfig)
r.SetLogger(logger)

// 关闭日志（在服务器关闭时）
defer r.CloseLogger()
```

## HTTP 客户端配置

Touka 内置了 `httpc` HTTP 客户端，可以在请求处理中方便地发起出站请求：

```go
// 创建自定义 HTTP 客户端
customClient := httpc.New()
r.SetHTTPClient(customClient)

// 在处理器中使用
r.GET("/proxy", func(c *touka.Context) {
    resp, err := c.GetHTTPC().Get("https://api.example.com/data")
    // ...
})
```

## 条件中间件

Touka 支持根据条件动态启用或禁用中间件：

```go
// 单个条件中间件
r.Use(r.UseIf(config.EnableLogging, AccessLoggerMiddleware()))

// 条件中间件链
r.Use(r.UseChainIf(config.EnableMetrics,
    MetricsMiddleware,
    PrometheusMiddleware,
    MonitoringMiddleware,
))
```

## 获取路由信息

```go
// 获取所有已注册的路由信息
routes := r.GetRouterInfo()
for _, route := range routes {
    fmt.Printf("Method: %s, Path: %s, Handler: %s, Group: %s\n",
        route.Method, route.Path, route.Handler, route.Group)
}
```
