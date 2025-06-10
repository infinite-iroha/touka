# Touka(灯花)框架

Touka(灯花) 是一个基于 Go 语言构建的多层次、高性能 Web 框架。其设计目标是为开发者提供**更直接的控制、有效的扩展能力，以及针对特定场景的行为优化**

## Touka 的设计特点

Touka 在一些特定方面进行了细致的设计与实现，旨在提供便利的工具与更清晰的控制：

*   **统一且可定制的错误处理**
    Touka 提供了灵活的错误处理机制，允许开发者通过 `Engine.SetErrorHandler` 设置统一的错误响应逻辑。此机制不仅适用于框架内部产生的错误，更特别之处在于它能够**捕获由 `http.FileServer` 等标准库处理器返回的 404 Not Found、403 Forbidden 等错误状态码**。
    *   **设计考量：** 默认情况下，`http.FileServer` 在文件未找到或权限不足时会直接返回标准错误响应。Touka 的设计能够拦截这些由 `http.FileServer` 发出的错误信号，并将其转发给框架统一的 `ErrorHandler`。这使得开发者可以为文件服务中的异常情况提供**与应用其他部分风格一致的自定义错误响应**，从而提升整体的用户体验和错误管理效率。

*   **客户端 IP 来源的透明解析**
    Touka 提供了可配置的客户端 IP 获取机制。开发者可以通过 `Engine.SetRemoteIPHeaders` 指定框架优先从哪些 HTTP 头部（如 `X-Forwarded-For`、`X-Real-IP`）解析客户端真实 IP，并通过 `Engine.SetForwardByClientIP` 控制此机制的启用。
    *   **实现细节：** `Context.RequestIP()` 方法会根据这些配置，从 `http.Request.Header` 中解析并返回第一个有效的 IP 地址。如果未配置或头部中未找到有效 IP，则回退到 `http.Request.RemoteAddr`，并对 IP 格式进行验证。这有助于在存在多层代理的环境中获取准确的源 IP。

*   **内置日志与出站 HTTP 客户端的 Context 绑定**
    Touka 的核心 `Context` 对象直接包含了对 `reco.Logger`（一个异步、结构化日志库）和 `httpc.Client`（一个功能增强的 HTTP 客户端）的引用。开发者可以直接通过 `c.GetLogger()` 和 `c.Client()` 在请求处理函数中访问这些工具。
    *   **设计考量：** 这种集成方式旨在提供这些核心工具在**特定请求生命周期内的统一访问点**。所有日志记录和出站 HTTP 请求操作都与当前请求上下文绑定，并能利用框架层面的全局配置，有助于简化复杂请求处理场景下的代码组织。

*   **强健的 Panic 恢复与连接状态感知**
    Touka 提供的 `Recovery` 中间件能够捕获处理链中的 `panic`。它会记录详细的堆栈信息和请求快照。此外，它能**识别由客户端意外断开连接**引起的网络错误（如 `broken pipe` 或 `connection reset by peer`），在这些情况下，框架会避免尝试向已失效的连接写入响应。
    *   **设计考量：** 这有助于防止因底层网络问题或客户端行为导致的二次 `panic`，避免在关闭的连接上进行无效写入，从而提升服务的稳定性。

*   **HTTP 协议版本与服务器行为的细致控制**
    Touka 允许开发者通过 `Engine.SetProtocols` 方法，精确定义服务器支持的 HTTP 协议版本（HTTP/1.1、HTTP/2、H2C）。框架也提供了对重定向行为、未匹配路由处理和文件服务行为的配置选项。
    *   **设计考量：** 这种协议和行为的细致化控制，为开发者提供了在特定部署环境（如 gRPC-Web 对 HTTP/2 的要求）中对服务器通信栈进行调整的能力。

*   **Context 对象的高效复用**
    Touka 对其核心 `Context` 对象进行了池化管理。每个请求处理结束后，`Context` 对象会被重置并返回到对象池中，以便后续请求复用。
    *   **设计考量：** 这种机制旨在减少每次请求的内存分配和垃圾回收（GC）压力，尤其在高并发场景下，有助于提供更平滑和可预测的性能表现。

### 快速上手

```go
package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/fenthope/reco"
	"github.com/infinite-iroha/touka"
)

func main() {
	r := touka.New()

	// 配置日志记录器 (可选，不设置则使用默认配置)
	logConfig := reco.Config{
		Level:      reco.LevelDebug,
		Mode:       reco.ModeText, // 或 reco.ModeJSON
		Output:     os.Stdout,
		Async:      true,
		BufferSize: 4096,
	}
	r.SetLogger(logConfig)

	// 配置统一错误处理器
	// Touka 允许您为 404, 500 等错误定义统一的响应。
	// 特别地，它能捕获 http.FileServer 产生的 404/403 错误并统一处理。
	r.SetErrorHandler(func(c *touka.Context, code int) {
		// 这里可以根据 code 返回 JSON, HTML, 或其他自定义错误页面
		c.JSON(code, touka.H{"error_code": code, "message": http.StatusText(code)})
		c.GetLogger().Errorf("发生HTTP错误: %d, 路径: %s", code, c.Request.URL.Path) // 记录错误
	})

	// 注册基本路由
	r.GET("/hello", func(c *touka.Context) {
		// 设置响应头部
		c.SetHeader("X-Framework", "Touka") // 设置一个头部
		c.AddHeader("X-Custom-Info", "Hello") // 添加一个头部 (如果已有则追加)
		c.AddHeader("X-Custom-Info", "World") // 再次添加，Content-Type: X-Custom-Info: Hello, World

		// 获取请求头部
		acceptEncoding := c.GetReqHeader("Accept-Encoding")
		userAgent := c.UserAgent() // 便捷获取 User-Agent

		c.String(http.StatusOK, "Hello from Touka! Your Accept-Encoding: %s, User-Agent: %s", acceptEncoding, userAgent)
		c.GetLogger().Infof("请求 /hello 来自 IP: %s", c.ClientIP())
	})

	r.GET("/json", func(c *touka.Context) {
		// 删除响应头部
		c.DelHeader("X-Powered-By") // 假设有这个头部，可以删除它
		c.JSON(http.StatusOK, touka.H{"message": "Welcome to Touka", "timestamp": time.Now()})
	})

	// 注册包含路径参数的路由
	r.GET("/user/:id", func(c *touka.Context) {
		userID := c.Param("id") // 获取路径参数
		c.String(http.StatusOK, "User ID: %s", userID)
	})

	// 注册使用查询参数的路由
	r.GET("/search", func(c *touka.Context) {
		query := c.DefaultQuery("q", "default_query") // 获取查询参数，提供默认值
		paramB := c.Query("paramB")                  // 获取另一个查询参数
		c.String(http.StatusOK, "Search query: %s, Param B: %s", query, paramB)
	})
    
    // 注册处理 POST 表单的路由
    r.POST("/submit-form", func(c *touka.Context) {
        name := c.PostForm("name") // 获取表单字段值
        email := c.DefaultPostForm("email", "no_email@example.com") // 获取表单字段，提供默认值
        c.String(http.StatusOK, "Form submitted: Name=%s, Email=%s", name, email)
    })

    // 演示 Set 和 Get 方法在中间件中传递数据
    // 在中间件中 Set 数据
    r.Use(func(c *touka.Context) {
        c.Set("requestID", "req-12345") // 设置一个数据
        c.Next()
    })
    // 在路由处理函数中 Get 数据
    r.GET("/context-data", func(c *touka.Context) {
        requestID, exists := c.Get("requestID") // 获取数据
        if !exists {
            requestID = "N/A"
        }
        c.String(http.StatusOK, "Request ID from Context: %s", requestID)
    })

	// 服务静态文件
	// 使用 r.Static 方法，其错误（如 404）将由上面设置的 ErrorHandler 统一处理
	// 假设您的静态文件在项目根目录的 'static' 文件夹
	r.Static("/static", "./static")

	// 演示出站 HTTP 请求 (使用 Context 中绑定的 httpc.Client)
	r.GET("/fetch-example", func(c *touka.Context) {
		resp, err := c.Client().Get("https://example.com", httpc.WithTimeout(5*time.Second))
		if err != nil {
			c.Errorf("出站请求失败: %v", err) // 记录错误
			c.String(http.StatusInternalServerError, "Failed to fetch external resource")
			return
		}
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		c.String(http.StatusOK, "Fetched from example.com (first 100 bytes): %s...", bodyBytes[:min(len(bodyBytes), 100)])
	})

	// 演示 HTTP 协议控制
	// 默认已启用 HTTP/1.1。如果需要 HTTP/2，通常需在 TLS 模式下启用。
	// r.SetProtocols(&touka.ProtocolsConfig{
	// 	Http1:           true,
	// 	Http2:           true, // 启用 HTTP/2 (需要 HTTPS)
	// 	Http2_Cleartext: false,
	// })

	// 启动服务器 (支持优雅关闭)
	log.Println("Touka Server starting on :8080...")
	err := r.RunShutdown(":8080", 10*time.Second) // 优雅关闭超时10秒
	if err != nil {
		log.Fatalf("Touka server failed to start: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

## 中间件支持

### 内置

Recovery `r.Use(touka.Recovery())`

### fenthope

[访问日志-record](https://github.com/fenthope/record) 

[Gzip](https://github.com/fenthope/gzip)

[压缩-Compress(Deflate,Gzip,Zstd)](https://github.com/fenthope/compress)

[请求速率限制-ikumi](https://github.com/fenthope/ikumi)

[sessions](https://github.com/fenthope/sessions)

[jwt](https://github.com/fenthope/jwt)

[带宽限制](https://github.com/fenthope/toukautil/blob/main/bandwithlimiter.go)

## 文档与贡献

*   **API 文档：** 访问 [pkg.go.dev/github.com/infinite-iroha/touka](https://pkg.go.dev/github.com/infinite-iroha/touka) 查看完整的 API 参考
*   **贡献：** 我们欢迎任何形式的贡献，无论是错误报告、功能建议还是代码提交。请遵循项目的贡献指南

## 相关项目

[gin](https://github.com/gin-gonic/gin) 参考并引用了相关部分代码

[reco](https://github.com/fenthope/reco) 灯花框架的默认日志库

[httpc](https://github.com/WJQSERVER-STUDIO/httpc) 原[touka-httpc](https://github.com/satomitouka/touka-httpc), 一个现代化且易用的HTTP Client, 作为Touka框架Context携带的HTTPC

## 许可证

本项目使用MPL许可证

tree部分来自[gin](https://github.com/gin-gonic/gin)与[httprouter](https://github.com/julienschmidt/httprouter)

[WJQSERVER/httproute](https://github.com/WJQSERVER/httprouter)是本项目的前身(一个[httprouter](https://github.com/julienschmidt/httprouter)的fork版本)