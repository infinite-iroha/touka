# 上下文 (Context)

`touka.Context` 是 Touka 框架中最重要的结构。它携带了关于当前 HTTP 请求的所有必要信息，并提供了一系列方法来解析请求和构建响应。

## 请求数据解析

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

### JSON 绑定

Touka 提供了非常便捷的 JSON 绑定功能，它会自动解析请求体并填充到结构体中，同时进行基本的验证。

```go
type LoginRequest struct {
    User     string `json:"user" binding:"required"`
    Password string `json:"password" binding:"required"`
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

// HTML 模板
c.HTML(http.StatusOK, "index.tmpl", touka.H{"title": "Main website"})
```

### 文件与流

```go
// 服务本地文件
c.File("/local/file.go")

// 将文件内容作为响应体（不触发下载）
c.SetRespBodyFile(http.StatusOK, "config.json")

// 写入数据流
c.WriteStream(reader)
```

### 重定向

```go
c.Redirect(http.StatusMovedPermanently, "http://google.com/")
```

## 数据传递 (Keys/Values)

您可以在中间件和处理器之间共享数据。

```go
// 在中间件中设置
c.Set("user_id", 12345)

// 在处理器中获取
id, exists := c.Get("user_id")
val := c.MustGet("user_id").(int)
```

## 状态管理

- `c.Abort()`: 停止执行后续的处理器/中间件。
- `c.Next()`: 执行后续的处理链。这常用于中间件中，在执行完某些前置逻辑后，显式调用 `Next`，并在其返回后执行后置逻辑。

## 对象池化

为了提高性能，Touka 的 Context 对象是复用的。
**重要提示：不要在 Goroutine 中持久化持有 `touka.Context` 指针。如果您需要在 Goroutine 中使用请求数据，请务必在派生 Goroutine 前提取所需的值。**
