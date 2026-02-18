# 静态文件与资源

Touka 提供了多种方式来服务静态文件，这些方法都集成了 Touka 的统一错误处理机制。

## 服务本地目录

`StaticDir` 方法将 URL 路径映射到本地文件系统目录。

```go
// 访问 /assets/js/main.js 将读取 ./static/js/main.js
r.StaticDir("/assets", "./static")
```

## 服务单个文件

`StaticFile` 用于将特定的 URL 映射到单个本地文件。

```go
r.StaticFile("/favicon.ico", "./resources/favicon.ico")
```

## 集成 Go 嵌入式资源 (embed.FS)

使用 Go 1.16+ 的 `embed` 特性，您可以将整个静态前端项目编译进二进制文件中。

```go
//go:embed dist/*
var content embed.FS

func main() {
    r := touka.Default()

    // 剥离 "dist" 前缀并包装为 http.FS
    fsroot, _ := fs.Sub(content, "dist")

    // 使用 StaticFS 提供服务
    r.StaticFS("/static", http.FS(fsroot))

    // 您也可以使用 StaticFS 服务根路径
    // r.StaticFS("/", http.FS(fsroot))

    r.Run(":8080")
}
```

## 未匹配路径作为文件服务 (UnMatchFS)

这是一个独特的功能：当没有任何 API 路由匹配时，尝试从指定的文件系统中查找并返回文件。这非常适合用于单页应用（SPA）的部署。

```go
r := touka.New()
r.SetUnMatchFS(http.Dir("./frontend/dist"), true)

// API 路由
r.GET("/api/status", handleStatus)

// 如果请求 /index.html 且没有 /index.html 的路由，
// 则会从 ./frontend/dist/index.html 读取。
```

## 性能提示

对于高负载的静态资源分发，虽然 Touka 表现出色，但我们仍建议在生产环境中使用 Nginx 或 CDN 站在 Touka 前面来处理静态文件，让 Touka 专注于处理动态逻辑。
