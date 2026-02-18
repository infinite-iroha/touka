# 中间件 (Middleware)

中间件是运行在 HTTP 请求处理链中的函数。它们可以拦截请求、修改数据、控制流向（通过 `c.Next()` 或 `c.Abort()`），并执行通用的前置/后置逻辑。

## 如何使用中间件

### 全局中间件

全局中间件应用于所有注册的路由。

```go
r := touka.New()
r.Use(touka.Recovery()) // 崩溃恢复
r.Use(MyCustomLogger()) // 自定义日志
```

### 路由组中间件

仅应用于特定组下的路由。

```go
api := r.Group("/api")
api.Use(AuthMiddleware())
{
    api.GET("/user", handleUser)
}
```

## 编写自定义中间件

中间件的函数签名是 `touka.HandlerFunc`。

### 示例：请求计时器

```go
func TimerMiddleware() touka.HandlerFunc {
    return func(c *touka.Context) {
        // --- 前置逻辑 ---
        start := time.Now()

        // 执行处理链中的下一个函数
        c.Next()

        // --- 后置逻辑 ---
        duration := time.Since(start)
        log.Printf("Request %s %s took %v", c.Request.Method, c.Request.URL.Path, duration)
    }
}
```

### 示例：简单的 API 密钥验证

```go
func APIKeyAuth() touka.HandlerFunc {
    return func(c *touka.Context) {
        apiKey := c.GetHeader("X-API-KEY")
        if apiKey != "secret-token" {
            // 验证失败，返回错误并中止后续逻辑
            c.JSON(http.StatusUnauthorized, touka.H{"error": "Invalid API Key"})
            c.Abort()
            return
        }

        // 验证通过，继续执行
        c.Next()
    }
}
```

## 内置中间件

- **Recovery**: 捕获任何发生的 panic，恢复运行并返回 500 错误。它还负责调用全局错误处理器。

Touka 的设计非常精简，许多扩展功能（如 Gzip, JWT, Sessions）由外部或第三方库提供，您可以轻松通过 `r.Use()` 集成它们。

## 条件中间件 (Conditional Middleware)

Touka 支持根据布尔条件动态启用或禁用中间件。这在根据环境配置启用插件时非常有用。

### `UseIf`

```go
// 仅在 Debug 模式下启用日志
r.Use(r.UseIf(config.IsDebug, MyDebugLogger))
```

### `UseChainIf` (条件中间件链)

如果您有一组相关的中间件需要根据同一条件启用，可以使用 `UseChainIf`。

```go
r.Use(r.UseChainIf(config.EnableMetrics,
    MetricsMiddleware,
    PrometheusMiddleware,
    MonitoringMiddleware,
))
```

这些方法利用了 `MiddlewareXFunc`（即返回 `HandlerFunc` 的工厂函数），确保中间件实例按需创建或高效复用。
