# 快速开始

本指南将帮助您在几分钟内启动并运行一个 Touka 应用。

## 安装

确保您的环境中已经安装了 Go 1.25 或更高版本。

在您的项目目录中运行：

```bash
go get github.com/infinite-iroha/touka
```

## 基础示例

创建一个 `main.go` 文件，并粘贴以下代码：

```go
package main

import (
    "net/http"
    "time"
    "log"
    "github.com/infinite-iroha/touka"
)

func main() {
    // 1. 创建默认引擎（包含 Recovery 中间件）
    r := touka.Default()

    // 2. 注册一个简单的 GET 路由
    r.GET("/ping", func(c *touka.Context) {
        c.JSON(http.StatusOK, touka.H{
            "message": "pong",
            "time":    time.Now().Unix(),
        })
    })

    // 3. 注册带参数的路由
    r.GET("/hello/:name", func(c *touka.Context) {
        name := c.Param("name")
        c.String(http.StatusOK, "Hello, %s!", name)
    })

    // 4. 启动服务器并监听 8080 端口
    log.Println("Touka server is running on :8080")
    if err := r.Run(":8080"); err != nil {
        log.Fatalf("Server failed: %v", err)
    }
}
```

## 运行应用

执行以下命令启动服务器：

```bash
go run main.go
```

现在，您可以访问：
- `http://localhost:8080/ping`
- `http://localhost:8080/hello/World`

## 优雅停机

在生产环境中，我们推荐使用 `RunShutdown` 方法来启动服务器，它会监听系统信号并在关闭前等待正在处理的请求完成。

```go
// 等待 10 秒以处理剩余请求
if err := r.RunShutdown(":8080", 10*time.Second); err != nil {
    log.Fatalf("Server forced to shutdown: %v", err)
}
```
