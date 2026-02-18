# Touka(灯花)框架

Touka(灯花) 是一个基于 Go 语言构建的多层次、高性能 Web 框架。其设计目标是为开发者提供**更直接的控制、有效的扩展能力，以及针对特定场景的行为优化**。

## 文档

我们提供了详尽的文档来帮助您快速上手并深入了解 Touka：

- **[灯花框架简介 (introduction.md)](docs/introduction.md)**
- **[快速开始 (quickstart.md)](docs/quickstart.md)**
- **[路由系统 (routing.md)](docs/routing.md)**
- **[上下文 Context (context.md)](docs/context.md)**
- **[中间件 (middleware.md)](docs/middleware.md)**
- **[统一错误处理 (error-handling.md)](docs/error-handling.md)**
- **[静态文件与资源 (static-files.md)](docs/static-files.md)**
- **[Server-Sent Events (sse.md)](docs/sse.md)**
- **[高级特性与优化 (advanced.md)](docs/advanced.md)**

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
	r := touka.Default() // 使用带 Recovery 中间件的默认引擎

	// 配置日志记录器 (可选)
	logConfig := reco.Config{
		Level:      reco.LevelDebug,
		Mode:       reco.ModeText,
		Output:     os.Stdout,
		Async:      true,
	}
	r.SetLoggerCfg(logConfig)

	// 配置统一错误处理器
	r.SetErrorHandler(func(c *touka.Context, code int, err error) {
		c.JSON(code, touka.H{"error_code": code, "message": http.StatusText(code)})
		c.GetLogger().Errorf("发生HTTP错误: %d, 路径: %s, 错误: %v", code, c.Request.URL.Path, err)
	})

	// 注册路由
	r.GET("/hello/:name", func(c *touka.Context) {
		name := c.Param("name")
		query := c.DefaultQuery("mood", "happy")
		c.String(http.StatusOK, "Hello, %s! You seem %s.", name, query)
	})

	// 启动服务器 (支持优雅关闭)
	log.Println("Touka Server starting on :8080...")
	if err := r.RunShutdown(":8080", 10*time.Second); err != nil {
		log.Fatalf("Touka server failed to start: %v", err)
	}
}
```

## 中间件支持

### 内置

- **Recovery:** `r.Use(touka.Recovery())` (已包含在 `touka.Default()` 中)

### 第三方 (fenthope)

- [访问日志-record](https://github.com/fenthope/record)
- [Gzip](https://github.com/fenthope/gzip)
- [压缩-Compress(Deflate,Gzip,Zstd)](https://github.com/fenthope/compress)
- [请求速率限制-ikumi](https://github.com/fenthope/ikumi)
- [sessions](https://github.com/fenthope/sessions)
- [jwt](https://github.com/fenthope/jwt)
- [带宽限制](https://github.com/fenthope/toukautil/blob/main/bandwithlimiter.go)

## 贡献

我们欢迎任何形式的贡献，无论是错误报告、功能建议还是代码提交。请遵循项目的贡献指南。

## 相关项目

- [gin](https://github.com/gin-gonic/gin): Touka 在路由和 API 设计上参考了 Gin。
- [reco](https://github.com/fenthope/reco): Touka 框架的默认日志库。
- [httpc](https://github.com/WJQSERVER-STUDIO/httpc): 一个现代化且易用的 HTTP Client，作为 Touka 框架 Context 携带的 HTTPC。

## 许可证

本项目基于 [Mozilla Public License, v. 2.0](https://mozilla.org/MPL/2.0/) 许可。

`tree.go` 部分代码源自 [gin](https://github.com/gin-gonic/gin) 与 [httprouter](https://github.com/julienschmidt/httprouter)，其原始许可为 BSD-style。
