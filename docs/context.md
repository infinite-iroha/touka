# 上下文 (Context)

`touka.Context` 是 Touka 框架中最重要的结构。它携带了关于当前 HTTP 请求的所有必要信息，并提供了一系列方法来解析请求和构建响应。

## 请求数据解析

### 路径参数 (Path Parameters)

```go
// 路由: /users/:id
r.GET("/users/:id", func(c *touka.Context) {
    id := c.Param("id")
    c.String(http.StatusOK, "User ID: %s", id)
})
```

### 查询参数 (Query Parameters)

```go
// /welcome?firstname=Jane&lastname=Doe
r.GET("/welcome", func(c *touka.Context) {
    firstname := c.DefaultQuery("firstname", "Guest")
    lastname := c.Query("lastname") // 快捷方式，不存在则为空

    c.String(http.StatusOK, "Hello %s %s", firstname, lastname)
})
```

### 表单数据 (Form Data)

```go
r.POST("/form_post", func(c *touka.Context) {
    message := c.PostForm("message")
    nick := c.DefaultPostForm("nick", "anonymous")

    c.JSON(http.StatusOK, touka.H{
        "status":  "posted",
        "message": message,
        "nick":    nick,
    })
})
```

### 请求体读取

```go
// 读取完整请求体
r.POST("/raw", func(c *touka.Context) {
    body, err := c.GetReqBodyFull()
    if err != nil {
        c.ErrorUseHandle(http.StatusBadRequest, err)
        return
    }
    c.Raw(http.StatusOK, "application/octet-stream", body)
})

// 获取 io.ReadCloser（只能读取一次）
r.POST("/stream", func(c *touka.Context) {
    reader := c.GetReqBody()
    defer reader.Close()
    // 处理 reader...
})
```

### 客户端信息

```go
r.GET("/client-info", func(c *touka.Context) {
    // 获取客户端 IP（支持代理转发）
    ip := c.RequestIP()
    // 或使用别名
    ip = c.ClientIP()

    // 获取 User-Agent
    ua := c.UserAgent()

    // 获取 Content-Type
    ct := c.ContentType()

    // 获取请求协议
    proto := c.GetProtocol()

    c.JSON(http.StatusOK, touka.H{
        "ip":        ip,
        "userAgent": ua,
        "protocol":  proto,
    })
})
```

### 请求头

```go
r.GET("/headers", func(c *touka.Context) {
    // 获取单个请求头
    auth := c.GetReqHeader("Authorization")

    // 获取所有请求头
    allHeaders := c.GetAllReqHeader()

    c.JSON(http.StatusOK, touka.H{
        "authorization": auth,
        "allHeaders":    allHeaders,
    })
})
```

## 数据绑定

### JSON 绑定

Touka 提供了非常便捷的 JSON 绑定功能，它会自动解析请求体并填充到结构体中。

```go
type LoginRequest struct {
    User     string `json:"user"`
    Password string `json:"password"`
}

r.POST("/login", func(c *touka.Context) {
    var json LoginRequest
    if err := c.ShouldBindJSON(&json); err != nil {
        c.JSON(http.StatusBadRequest, touka.H{"error": err.Error()})
        return
    }

    if json.User != "admin" || json.Password != "123" {
        c.JSON(http.StatusUnauthorized, touka.H{"status": "unauthorized"})
        return
    }

    c.JSON(http.StatusOK, touka.H{"status": "you are logged in"})
})
```

### 表单绑定

```go
type UserForm struct {
    Name  string `form:"name"`
    Email string `form:"email"`
    Age   int    `form:"age"`
}

r.POST("/user", func(c *touka.Context) {
    var form UserForm
    if err := c.ShouldBindForm(&form); err != nil {
        c.JSON(http.StatusBadRequest, touka.H{"error": err.Error()})
        return
    }
    c.JSON(http.StatusOK, form)
})
```

### 通用绑定

`ShouldBind` 方法会根据请求的 `Content-Type` 自动选择绑定方式：

```go
r.POST("/data", func(c *touka.Context) {
    var data MyData
    // 自动根据 Content-Type 绑定（支持 JSON、Form、WANF、GOB）
    if err := c.ShouldBind(&data); err != nil {
        c.JSON(http.StatusBadRequest, touka.H{"error": err.Error()})
        return
    }
    c.JSON(http.StatusOK, data)
})
```

### WANF 绑定

```go
r.POST("/wanf", func(c *touka.Context) {
    var data MyData
    if err := c.ShouldBindWANF(&data); err != nil {
        c.JSON(http.StatusBadRequest, touka.H{"error": err.Error()})
        return
    }
    c.JSON(http.StatusOK, data)
})
```

### GOB 绑定

```go
r.POST("/gob", func(c *touka.Context) {
    var data MyData
    if err := c.ShouldBindGOB(&data); err != nil {
        c.JSON(http.StatusBadRequest, touka.H{"error": err.Error()})
        return
    }
    c.JSON(http.StatusOK, data)
})
```

## 响应构建

### 基础格式

Touka 支持多种响应格式：

```go
// JSON
c.JSON(http.StatusOK, touka.H{"message": "hey"})

// 字符串 (支持格式化)
c.String(http.StatusOK, "welcome %s", name)

// 纯文本
c.Text(http.StatusOK, "just text")

// 原始数据
c.Raw(http.StatusOK, "application/octet-stream", []byte("raw bytes"))

// HTML 模板
c.HTML(http.StatusOK, "index.tmpl", touka.H{"title": "Main website"})
```

### WANF 响应

```go
// WANF 格式响应
c.WANF(http.StatusOK, touka.H{"message": "wanf format"})
```

### GOB 响应

```go
// GOB 格式响应
c.GOB(http.StatusOK, myData)
```

### 文件与流

```go
// 服务本地文件（触发浏览器下载）
c.File("/local/file.go")

// 将文件内容作为响应体（不触发下载）
c.SetRespBodyFile(http.StatusOK, "config.json")

// 以文本形式发送文件
c.FileText(http.StatusOK, "/path/to/file.txt")

// 写入数据流
c.WriteStream(reader)

// 设置响应体为流
c.SetBodyStream(reader, contentSize) // contentSize 为 -1 表示未知大小
```

### 响应头操作

```go
// 设置响应头
c.SetHeader("X-Custom-Header", "value")

// 添加响应头（不覆盖已有值）
c.AddHeader("X-Custom-Header", "another-value")

// 删除响应头
c.DelHeader("X-Custom-Header")

// 批量设置响应头
c.SetHeaders(map[string][]string{
    "X-Header-1": {"value1"},
    "X-Header-2": {"value2a", "value2b"},
})

// 获取所有响应头
headers := c.GetAllRespHeader()
```

### 状态码

```go
// 设置状态码（不写入响应体）
c.Status(http.StatusNoContent)
```

### 重定向

```go
c.Redirect(http.StatusMovedPermanently, "http://google.com/")
```

## Cookie 操作

```go
// 设置 Cookie
c.SetCookie("session_id", "abc123", 3600, "/", "example.com", true, true)

// 设置 SameSite 属性
c.SetSameSite(http.SameSiteStrictMode)

// 使用完整 Cookie 对象
cookie := &http.Cookie{
    Name:  "token",
    Value: "xyz",
    Path:  "/",
}
c.SetCookieData(cookie)

// 获取 Cookie
value, err := c.GetCookie("session_id")
if err != nil {
    c.String(http.StatusUnauthorized, "Cookie not found")
    return
}

// 删除 Cookie
c.DeleteCookie("session_id")
```

## 数据传递 (Keys/Values)

您可以在中间件和处理器之间共享数据。

```go
// 在中间件中设置
c.Set("user_id", 12345)

// 在处理器中获取
id, exists := c.Get("user_id")
val := c.MustGet("user_id").(int)

// 类型安全的获取方法
str, exists := c.GetString("key")
i, exists := c.GetInt("key")
b, exists := c.GetBool("key")
f, exists := c.GetFloat64("key")
t, exists := c.GetTime("key")
d, exists := c.GetDuration("key")
```

## 错误处理

```go
r.GET("/error", func(c *touka.Context) {
    // 添加错误到上下文（可以添加多个）
    c.AddError(errors.New("error 1"))
    c.AddError(errors.New("error 2"))

    // 获取所有错误
    errs := c.GetErrors()

    // 使用全局错误处理器
    c.ErrorUseHandle(http.StatusInternalServerError, errors.New("something went wrong"))
})
```

## 日志记录

Touka 集成了 `reco` 日志库，可以直接在 Context 中使用：

```go
r.GET("/log", func(c *touka.Context) {
    c.Debugf("Debug message: %s", "details")
    c.Infof("User accessed /log")
    c.Warnf("Warning: %v", someWarning)
    c.Errorf("Error occurred: %v", someError)

    // 获取底层日志器
    logger := c.GetLogger()
    logger.CustomLog("level", "message")

    c.String(http.StatusOK, "Logged")
})
```

## HTTP 客户端

Touka 集成了 `httpc` HTTP 客户端，方便发起出站请求：

```go
r.GET("/proxy", func(c *touka.Context) {
    // 获取 HTTP 客户端
    client := c.GetHTTPC()
    // 或
    client = c.Client()

    // 发起请求
    resp, err := client.Get("https://api.example.com/data")
    if err != nil {
        c.ErrorUseHandle(http.StatusBadGateway, err)
        return
    }
    defer resp.Body.Close()

    // 将响应流式传输给客户端
    c.SetHeader("Content-Type", resp.Header.Get("Content-Type"))
    c.WriteStream(resp.Body)
})
```

## 状态管理

- `c.Abort()`: 停止执行后续的处理器/中间件。
- `c.AbortWithStatus(code)`: 中止并设置状态码。
- `c.IsAborted()`: 检查是否已中止。
- `c.Next()`: 执行后续的处理链。这常用于中间件中，在执行完某些前置逻辑后，显式调用 `Next`，并在其返回后执行后置逻辑。

## 请求上下文 (Go Context)

Touka Context 实现了 Go 标准库的 `context.Context` 接口：

```go
r.GET("/long-task", func(c *touka.Context) {
    // 获取 Go context
    ctx := c.Context()

    // 监听取消信号
    select {
    case <-ctx.Done():
        // 客户端断开连接或超时
        return
    case result := <-doLongTask(ctx):
        c.JSON(http.StatusOK, result)
    }
})

// 其他 context 方法
done := c.Done()      // 获取 Done channel
err := c.Err()        // 获取错误
val := c.Value("key") // 获取值（同时查找 Keys 和 Go context）
```

## 其他方法

```go
// 获取原始请求 URI
uri := c.GetRequestURI()

// 获取请求路径
path := c.GetRequestURIPath()

// 获取查询字符串
query := c.GetReqQueryString()

// 获取请求协议版本
proto := c.GetProtocol() // 例如 "HTTP/1.1"
```

## 对象池化

为了提高性能，Touka 的 Context 对象是复用的。

**重要提示：不要在 Goroutine 中持久化持有 `touka.Context` 指针。如果您需要在 Goroutine 中使用请求数据，请务必在派生 Goroutine 前提取所需的值。**

```go
// 错误示例 ❌
r.GET("/bad", func(c *touka.Context) {
    go func() {
        time.Sleep(5 * time.Second)
        // 此时 c 可能已被复用，数据不安全
        log.Println(c.Query("name"))
    }()
})

// 正确示例 ✓
r.GET("/good", func(c *touka.Context) {
    name := c.Query("name") // 提前提取值
    go func() {
        time.Sleep(5 * time.Second)
        log.Println(name) // 使用提取的值，安全
    }()
})
```
