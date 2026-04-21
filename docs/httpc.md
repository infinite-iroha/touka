# HTTP Client (httpc)

Touka 内置了 [httpc](https://github.com/WJQSERVER-STUDIO/httpc) HTTP 客户端，方便在请求处理函数中发起出站 HTTP 请求。

## 核心特性

- **自动 Context 关联**：使用 `HTTPC()` 方法时，出站请求会自动关联当前请求的 Context
- **请求取消传播**：当客户端断开连接时，出站请求会自动取消，避免资源泄漏
- **链式调用**：保持 httpc 原有的组合式构建器风格

## 基本用法

### 简单 GET 请求

```go
r.GET("/proxy", func(c *touka.Context) {
    body, err := c.HTTPC().
        GET("https://api.example.com/data").
        Text()
    if err != nil {
        c.JSON(500, touka.H{"error": err.Error()})
        return
    }
    c.String(200, body)
})
```

### POST JSON 请求

```go
r.POST("/users", func(c *touka.Context) {
    var req struct {
        Name  string `json:"name"`
        Email string `json:"email"`
    }
    c.ShouldBindJSON(&req)

    var result struct {
        ID   int    `json:"id"`
        Name string `json:"name"`
    }

    err := c.HTTPC().
        POST("https://api.example.com/users").
        SetHeader("Authorization", "Bearer "+token).
        SetJSONBody(req).
        DecodeJSON(&result)
    if err != nil {
        c.JSON(500, touka.H{"error": err.Error()})
        return
    }
    c.JSON(200, result)
})
```

### 带查询参数

```go
r.GET("/search", func(c *touka.Context) {
    query := c.Query("q")

    var result SearchResult
    err := c.HTTPC().
        GET("https://api.example.com/search").
        SetQueryParam("q", query).
        SetQueryParam("limit", "10").
        DecodeJSON(&result)
    if err != nil {
        c.JSON(500, touka.H{"error": err.Error()})
        return
    }
    c.JSON(200, result)
})
```

## API 对比

### 旧方式（Deprecated）

```go
// 需要手动 WithContext，容易忘记
resp, err := c.Client().
    WithContext(c.Context()).
    GET(url).
    Execute()
```

### 新方式（推荐）

```go
// 自动关联请求 Context
resp, err := c.HTTPC().
    GET(url).
    Execute()
```

## Context 取消机制

使用 `HTTPC()` 时，当客户端断开连接（如关闭浏览器），出站请求会自动取消：

```go
r.GET("/long-task", func(c *touka.Context) {
    // 这个请求会在客户端断开时自动取消
    resp, err := c.HTTPC().
        GET("https://slow-api.example.com/data").
        Execute()
    
    // 如果客户端已断开，err 会包含 context.Canceled
    if errors.Is(err, context.Canceled) {
        return // 客户端已断开，无需处理
    }
    // ...
})
```

## 完整 API

### contextHTTPClient 方法

| 方法 | 返回类型 | 说明 |
|------|----------|------|
| `NewRequestBuilder(method, url)` | `*httpc.RequestBuilder` | 创建通用请求构建器 |
| `GET(url)` | `*httpc.RequestBuilder` | 创建 GET 请求 |
| `POST(url)` | `*httpc.RequestBuilder` | 创建 POST 请求 |
| `PUT(url)` | `*httpc.RequestBuilder` | 创建 PUT 请求 |
| `DELETE(url)` | `*httpc.RequestBuilder` | 创建 DELETE 请求 |
| `PATCH(url)` | `*httpc.RequestBuilder` | 创建 PATCH 请求 |
| `HEAD(url)` | `*httpc.RequestBuilder` | 创建 HEAD 请求 |
| `OPTIONS(url)` | `*httpc.RequestBuilder` | 创建 OPTIONS 请求 |

### httpc.RequestBuilder 链式方法

返回 `*httpc.RequestBuilder`（用于链式调用）：

| 方法 | 说明 |
|------|------|
| `WithContext(ctx)` | 设置 Context（通常不需要，已自动关联） |
| `NoDefaultHeaders()` | 不添加默认 Header |
| `SetHeader(key, value)` | 设置 Header |
| `AddHeader(key, value)` | 添加 Header（可重复） |
| `SetHeaders(map)` | 批量设置 Headers |
| `SetQueryParam(key, value)` | 设置查询参数 |
| `AddQueryParam(key, value)` | 添加查询参数（可重复） |
| `SetQueryParams(map)` | 批量设置查询参数 |
| `SetBody(io.Reader)` | 设置请求 Body |
| `SetRawBody([]byte)` | 设置字节 Body |

返回 `(*httpc.RequestBuilder, error)`（可能失败）：

| 方法 | 说明 |
|------|------|
| `SetJSONBody(any)` | 设置 JSON Body |
| `SetXMLBody(any)` | 设置 XML Body |
| `SetGOBBody(any)` | 设置 GOB Body |

### 终结方法

| 方法 | 返回类型 | 说明 |
|------|----------|------|
| `Build()` | `(*http.Request, error)` | 构建请求但不执行 |
| `Execute()` | `(*http.Response, error)` | 执行并返回原始响应 |
| `DecodeJSON(v)` | `error` | 执行并解码 JSON |
| `DecodeXML(v)` | `error` | 执行并解码 XML |
| `DecodeGOB(v)` | `error` | 执行并解码 GOB |
| `Text()` | `(string, error)` | 执行并返回文本 |
| `Bytes()` | `([]byte, error)` | 执行并返回字节 |
| `SSE()` | `(*SSEStream, error)` | 建立 SSE 流连接 |

## 迁移指南

### go:fix inline 兼容

旧代码 `c.GetHTTPC()` 可通过 `go fix` 自动迁移到 `c.Client()`：

```bash
go fix ./...
```

### 手动迁移

| 旧代码 | 新代码 |
|--------|--------|
| `c.GetHTTPC()` | `c.Client()` 或 `c.HTTPC()` |
| `c.Client().WithContext(ctx).GET(url)` | `c.HTTPC().GET(url)` |

## 示例

完整示例请参考 [examples/httpc](../examples/httpc)。
