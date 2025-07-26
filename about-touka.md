# 关于 Touka (灯花) 框架：一份深度指南

Touka (灯花) 是一个基于 Go 语言构建的、功能丰富且高性能的 Web 框架。它的核心设计目标是为开发者提供一个既强大又灵活的工具集，允许对框架行为进行深度定制，同时通过精心设计的组件和机制，优化在真实业务场景中的开发体验和运行性能。

本文档旨在提供一份全面而深入的指南，帮助您理解 Touka 的核心概念、设计哲学以及如何利用其特性来构建健壮、高效的 Web 应用。

---

## 核心设计哲学

Touka 的设计哲学根植于以下几个核心原则：

*   **控制力与可扩展性:** 框架在提供强大默认功能的同时，也赋予开发者充分的控制权。我们相信开发者最了解自己的业务需求。因此，无论是路由行为、错误处理逻辑，还是服务器协议，都可以根据具体需求进行精细调整和扩展。
*   **明确性与可预测性:** API 设计力求直观和一致，使得框架的行为易于理解和预测，减少开发过程中的意外。我们避免使用过多的“魔法”，倾向于让代码的意图清晰可见。
*   **性能意识:** 在核心组件的设计中，性能是一个至关重要的考量因素。通过采用如对象池、优化的路由算法等技术，Touka 致力于在高并发场景下保持低延迟和高吞吐。
*   **开发者体验:** 框架内置了丰富的辅助工具和便捷的 API，例如与请求上下文绑定的日志记录器和 HTTP 客户端，旨在简化常见任务，提升开发效率。

---

## 核心功能深度剖析

### 1. 引擎 (Engine)：框架的中央枢纽

`Engine` 是 Touka 框架的实例，也是所有功能的入口和协调者。它实现了 `http.Handler` 接口，可以无缝集成到 Go 的标准 HTTP 生态中。

#### 1.1. 初始化引擎

```go
// 创建一个“干净”的引擎，不包含任何默认中间件
r := touka.New()

// 创建一个带有默认中间件的引擎，目前仅包含 Recovery()
// 推荐在生产环境中使用，以防止 panic 导致整个服务崩溃
r := touka.Default()
```

#### 1.2. 引擎配置

`Engine` 提供了丰富的配置选项，允许您定制其核心行为。

```go
func main() {
    r := touka.New()

    // === 路由行为配置 ===

    // 自动重定向尾部带斜杠的路径，默认为 true
    // e.g., /foo/ 会被重定向到 /foo
    r.SetRedirectTrailingSlash(true)

    // 自动修复路径的大小写，默认为 true
    // e.g., /FOO 会被重定向到 /foo (如果 /foo 存在)
    r.SetRedirectFixedPath(true)

    // 当路由存在但方法不匹配时，自动处理 405 Method Not Allowed，默认为 true
    r.SetHandleMethodNotAllowed(true)

    // === IP 地址解析配置 ===

    // 是否信任 X-Forwarded-For, X-Real-IP 等头部来获取客户端 IP，默认为 true
    // 在反向代理环境下非常有用
    r.SetForwardByClientIP(true)
    // 自定义用于解析 IP 的头部列表，按顺序查找
    r.SetRemoteIPHeaders([]string{"X-Forwarded-For", "X-App-Client-IP", "X-Real-IP"})

    // === 请求体大小限制 ===

    // 设置全局默认的请求体最大字节数，-1 表示不限制
    // 这有助于防止 DoS 攻击
    r.SetGlobalMaxRequestBodySize(10 * 1024 * 1024) // 10 MB

    // ... 其他配置
    r.Run(":8080")
}
```

#### 1.3. 服务器生命周期管理

Touka 提供了对底层 `*http.Server` 的完全控制，并内置了优雅关闭的逻辑。

```go
func main() {
    r := touka.New()

    // 通过 ServerConfigurator 对 http.Server 进行自定义配置
    r.SetServerConfigurator(func(server *http.Server) {
        // 设置自定义的读写超时时间
        server.ReadTimeout = 15 * time.Second
        server.WriteTimeout = 15 * time.Second
        fmt.Println("自定义的 HTTP 服务器配置已应用")
    })

    // 启动服务器，并支持优雅关闭
    // RunShutdown 会阻塞，直到收到 SIGINT 或 SIGTERM 信号
    // 第二个参数是优雅关闭的超时时间
    fmt.Println("服务器启动于 :8080")
    if err := r.RunShutdown(":8080", 10*time.Second); err != nil {
        log.Fatalf("服务器启动失败: %v", err)
    }
}
```

---

### 2. 路由系统 (Routing)：强大、灵活、高效

Touka 的路由系统基于一个经过优化的**基数树 (Radix Tree)**，它支持静态路径、路径参数和通配符，并能实现极高的查找性能。

#### 2.1. 基本路由

```go
// 精确匹配的静态路由
r.GET("/ping", func(c *touka.Context) {
    c.String(http.StatusOK, "pong")
})

// 注册多个 HTTP 方法
r.HandleFunc([]string{"GET", "POST"}, "/data", func(c *touka.Context) {
    c.String(http.StatusOK, "Data received via %s", c.Request.Method)
})

// 注册所有常见 HTTP 方法
r.ANY("/any", func(c *touka.Context) {
    c.String(http.StatusOK, "Handled with ANY for method %s", c.Request.Method)
})
```

#### 2.2. 参数化路由

使用冒号 `:` 来定义路径参数。

```go
r.GET("/users/:id", func(c *touka.Context) {
    // 通过 c.Param() 获取路径参数
    userID := c.Param("id")
    c.String(http.StatusOK, "获取用户 ID: %s", userID)
})

r.GET("/articles/:category/:article_id", func(c *touka.Context) {
    category := c.Param("category")
    articleID := c.Param("article_id")
    c.JSON(http.StatusOK, touka.H{
        "category": category,
        "id":       articleID,
    })
})
```

#### 2.3. 通配符路由 (Catch-all)

使用星号 `*` 来定义通配符路由，它会捕获该点之后的所有路径段。**通配符路由必须位于路径的末尾**。

```go
// 匹配如 /static/js/main.js, /static/css/style.css 等
r.GET("/static/*filepath", func(c *touka.Context) {
    // 捕获的路径可以通过参数名 "filepath" 获取
    filePath := c.Param("filepath")
    c.String(http.StatusOK, "请求的文件路径是: %s", filePath)
})
```

#### 2.4. 路由组 (RouterGroup)

路由组是组织和管理路由的强大工具，特别适用于构建结构化的 API。

```go
func main() {
    r := touka.New()

    // 所有 /api/v1 下的路由都需要经过 AuthMiddleware
    v1 := r.Group("/api/v1")
    v1.Use(AuthMiddleware()) // 应用组级别的中间件
    {
        // 匹配 /api/v1/products
        v1.GET("/products", getProducts)
        // 匹配 /api/v1/products/:id
        v1.GET("/products/:id", getProductByID)

        // 可以在组内再嵌套组
        ordersGroup := v1.Group("/orders")
        ordersGroup.Use(OrderPermissionsMiddleware()) // 更具体的中间件
        {
            // 匹配 /api/v1/orders
            ordersGroup.GET("", getOrders)
            // 匹配 /api/v1/orders/:id
            ordersGroup.GET("/:id", getOrderByID)
        }
    }

    r.Run(":8080")
}

func AuthMiddleware() touka.HandlerFunc {
    return func(c *touka.Context) {
        // 模拟认证逻辑
        fmt.Println("V1 Auth Middleware: Checking credentials...")
        c.Next()
    }
}
// ... 其他处理器
```

---

### 3. 上下文 (Context)：请求的灵魂

`touka.Context` 是框架中最为核心的结构，它作为每个 HTTP 请求的上下文，在中间件和最终处理器之间流转。它提供了海量的便捷 API 来简化开发。

#### 3.1. 请求数据解析

##### 获取查询参数

```go
// 请求 URL: /search?q=touka&lang=go&page=1
r.GET("/search", func(c *touka.Context) {
    // c.Query() 获取指定参数，不存在则返回空字符串
    query := c.Query("q") // "touka"

    // c.DefaultQuery() 获取参数，不存在则返回指定的默认值
    lang := c.DefaultQuery("lang", "en") // "go"
    category := c.DefaultQuery("cat", "all") // "all"

    c.JSON(http.StatusOK, touka.H{
        "query":    query,
        "language": lang,
        "category": category,
    })
})
```

##### 获取 POST 表单数据

```go
// 使用 curl 测试:
// curl -X POST http://localhost:8080/register -d "username=test&email=test@example.com"
r.POST("/register", func(c *touka.Context) {
    username := c.PostForm("username")
    email := c.DefaultPostForm("email", "anonymous@example.com")
    // 也可以获取所有表单数据
    // form, _ := c.Request.MultipartForm()

    c.String(http.StatusOK, "注册成功: 用户名=%s, 邮箱=%s", username, email)
})
```

##### JSON 数据绑定

Touka 可以轻松地将请求体中的 JSON 数据绑定到 Go 结构体。

```go
type UserProfile struct {
    Name    string   `json:"name" binding:"required"`
    Age     int      `json:"age" binding:"gte=18"`
    Tags    []string `json:"tags"`
    Address string   `json:"address,omitempty"`
}

// 使用 curl 测试:
// curl -X POST http://localhost:8080/profile -H "Content-Type: application/json" -d '''
// {
//   "name": "Alice",
//   "age": 25,
//   "tags": ["go", "web"]
// }
// '''
r.POST("/profile", func(c *touka.Context) {
    var profile UserProfile
    
    // c.ShouldBindJSON() 会解析 JSON 并填充到结构体中
    if err := c.ShouldBindJSON(&profile); err != nil {
        // 如果 JSON 格式错误或不满足绑定标签，会返回错误
        c.JSON(http.StatusBadRequest, touka.H{"error": err.Error()})
        return
    }

    c.JSON(http.StatusOK, touka.H{
        "status":  "success",
        "profile": profile,
    })
})
```

#### 3.2. 响应构建

##### 发送 JSON, String, Text

```go
r.GET("/responses", func(c *touka.Context) {
    // c.JSON(http.StatusOK, touka.H{"framework": "Touka"})
    // c.String(http.StatusOK, "Hello, %s", "World")
    c.Text(http.StatusOK, "This is plain text.")
})
```

##### 渲染 HTML 模板

首先，需要为引擎配置一个模板渲染器。

```go
// main.go
import "html/template"

func main() {
    r := touka.New()
    // 加载模板文件
    r.HTMLRender = template.Must(template.ParseGlob("templates/*.html"))

    r.GET("/index", func(c *touka.Context) {
        // 渲染 index.html 模板，并传入数据
        c.HTML(http.StatusOK, "index.html", touka.H{
            "title": "Touka 模板渲染",
            "user":  "Guest",
        })
    })

    r.Run(":8080")
}

// templates/index.html
// <h1>{{ .title }}</h1>
// <p>Welcome, {{ .user }}!</p>
```

##### 文件和流式响应

```go
// 直接发送一个文件
r.GET("/download/report", func(c *touka.Context) {
    // 浏览器会提示下载
    c.File("./reports/latest.pdf")
})

// 将文件内容作为响应体
r.GET("/show/config", func(c *touka.Context) {
    // 浏览器会直接显示文件内容（如果支持）
    c.SetRespBodyFile(http.StatusOK, "./config.yaml")
})

// 流式响应，适用于大文件或实时数据
r.GET("/stream", func(c *touka.Context) {
    // 假设 getRealTimeDataStream() 返回一个 io.Reader
    // dataStream := getRealTimeDataStream()
    // c.WriteStream(dataStream)
})
```

#### 3.3. Cookie 操作

Touka 提供了简单的 API 来管理 Cookie。

```go
r.GET("/login", func(c *touka.Context) {
    // 设置一个有效期为 1 小时的 cookie
    c.SetCookie("session_id", "user-12345", 3600, "/", "localhost", false, true)
    c.String(http.StatusOK, "登录成功！")
})

r.GET("/me", func(c *touka.Context) {
    sessionID, err := c.GetCookie("session_id")
    if err != nil {
        c.String(http.StatusUnauthorized, "请先登录")
        return
    }
    c.String(http.StatusOK, "您的会话 ID 是: %s", sessionID)
})

r.GET("/logout", func(c *touka.Context) {
    // 通过将 MaxAge 设置为 -1 来删除 cookie
    c.DeleteCookie("session_id")
    c.String(http.StatusOK, "已退出登录")
})
```

#### 3.4. 中间件数据传递

使用 `c.Set()` 和 `c.Get()` 可以在处理链中传递数据。

```go
// 中间件：生成并设置请求 ID
func RequestIDMiddleware() touka.HandlerFunc {
    return func(c *touka.Context) {
        requestID := fmt.Sprintf("req-%d", time.Now().UnixNano())
        c.Set("RequestID", requestID)
        c.Next()
    }
}

func main() {
    r := touka.New()
    r.Use(RequestIDMiddleware())

    r.GET("/status", func(c *touka.Context) {
        // 在处理器中获取由中间件设置的数据
        // c.MustGet() 在 key 不存在时会 panic，适用于确定存在的场景
        requestID := c.MustGet("RequestID").(string)
        
        // 或者使用安全的 Get
        // requestID, exists := c.GetString("RequestID")

        c.JSON(http.StatusOK, touka.H{"status": "ok", "request_id": requestID})
    })

    r.Run(":8080")
}
```

#### 3.5. 集成的工具

##### 日志记录

Touka 集成了 `reco` 日志库，可以直接在 `Context` 中使用。

```go
r.GET("/log-test", func(c *touka.Context) {
    userID := "user-abc"
    c.Infof("用户 %s 访问了 /log-test", userID)
    
    err := errors.New("一个模拟的错误")
    if err != nil {
        c.Errorf("处理请求时发生错误: %v, 用户: %s", err, userID)
    }

    c.String(http.StatusOK, "日志已记录")
})
```

##### HTTP 客户端

Touka 集成了 `httpc` 客户端，方便发起出站请求。

```go
r.GET("/fetch-data", func(c *touka.Context) {
    // 使用 Context 携带的 httpc 客户端
    resp, err := c.GetHTTPC().Get("https://api.github.com/users/WJQSERVER-STUDIO", httpc.WithTimeout(5*time.Second))
    if err != nil {
        c.ErrorUseHandle(http.StatusInternalServerError, err)
        return
    }
    defer resp.Body.Close()

    // 将外部响应直接流式传输给客户端
    c.SetHeader("Content-Type", resp.Header.Get("Content-Type"))
    c.WriteStream(resp.Body)
})
```

---

### 4. 错误处理：统一且强大

Touka 的一个标志性特性是其统一的错误处理机制。

#### 4.1. 自定义全局错误处理器

```go
func main() {
    r := touka.New()

    // 设置一个自定义的全局错误处理器
    r.SetErrorHandler(func(c *touka.Context, code int, err error) {
        // 检查是否是客户端断开连接
        if errors.Is(err, context.Canceled) {
            return // 不做任何事
        }

        // 记录详细错误
        c.GetLogger().Errorf("捕获到错误: code=%d, err=%v, path=%s", code, err, c.Request.URL.Path)

        // 根据错误码返回不同的响应
        switch code {
        case http.StatusNotFound:
            c.JSON(code, touka.H{"error": "您要找的页面去火星了"})
        case http.StatusMethodNotAllowed:
            c.JSON(code, touka.H{"error": "不支持的请求方法"})
        default:
            c.JSON(code, touka.H{"error": "服务器内部错误"})
        }
    })

    // 这个路由不存在，会触发 404
    // r.GET("/this-route-does-not-exist", ...)

    // 静态文件服务，如果文件不存在，也会被上面的 ErrorHandler 捕获
    r.StaticDir("/files", "./non-existent-dir")

    r.Run(":8080")
}
```

#### 4.2. `errorCapturingResponseWriter` 的魔力

Touka 如何捕获 `http.FileServer` 的错误？答案是 `errorCapturingResponseWriter`。

当您使用 `r.StaticDir` 或类似方法时，Touka 不会直接将 `http.FileServer` 作为处理器。相反，它会用一个自定义的 `ResponseWriter` 实现（即 `ecw`）来包装原始的 `ResponseWriter`，然后才调用 `http.FileServer.ServeHTTP`。

这个包装器会：
1.  **拦截 `WriteHeader(statusCode)` 调用：** 当 `http.FileServer` 内部决定要写入一个例如 `404 Not Found` 的状态码时，`ecw` 会捕获这个 `statusCode`。
2.  **判断是否为错误：** 如果 `statusCode >= 400`，`ecw` 会将此视为一个错误信号。
3.  **阻止原始响应：** `ecw` 会阻止 `http.FileServer` 继续向客户端写入任何内容（包括响应体）。
4.  **调用全局 `ErrorHandler`：** 最后，`ecw` 会调用您通过 `r.SetErrorHandler` 设置的全局错误处理器，并将捕获到的 `statusCode` 和一个通用错误传递给它。

这个机制确保了无论是动态 API 的错误还是静态文件服务的错误，都能被统一、优雅地处理，从而提供一致的用户体验。

---

### 5. 静态文件服务与嵌入式资源

#### 5.1. 服务本地文件

```go
// 将 URL /assets/ 映射到本地的 ./static 目录
r.StaticDir("/assets", "./static")

// 将 URL /favicon.ico 映射到本地的 ./static/img/favicon.ico 文件
r.StaticFile("/favicon.ico", "./static/img/favicon.ico")
```

#### 5.2. 服务嵌入式资源 (Go 1.16+)

使用 `go:embed` 可以将静态资源直接编译到二进制文件中，实现真正的单体应用部署。

```go
// main.go
package main

import (
    "embed"
    "io/fs"
    "net/http"
    "github.com/infinite-iroha/touka"
)

//go:embed frontend/dist
var embeddedFS embed.FS

func main() {
    r := touka.New()

    // 创建一个子文件系统，根目录为 embeddedFS 中的 frontend/dist
    subFS, err := fs.Sub(embeddedFS, "frontend/dist")
    if err != nil {
        panic(err)
    }

    // 使用 StaticFS 来服务这个嵌入式文件系统
    // 所有对 / 的访问都会映射到嵌入的 frontend/dist 目录
    r.StaticFS("/", http.FS(subFS))

    r.Run(":8080")
}
```

---

### 6. 与标准库的无缝集成

Touka 提供了适配器，可以轻松使用任何实现了标准 `http.Handler` 或 `http.HandlerFunc` 接口的组件。

```go
import "net/http/pprof"

// 适配一个标准的 http.HandlerFunc
r.GET("/legacy-handler", touka.AdapterStdFunc(func(w http.ResponseWriter, r *http.Request) {
    w.Write([]byte("这是一个标准的 http.HandlerFunc"))
}))

// 适配一个标准的 http.Handler，例如 pprof
debugGroup := r.Group("/debug/pprof")
{
    debugGroup.GET("/", touka.AdapterStdFunc(pprof.Index))
    debugGroup.GET("/cmdline", touka.AdapterStdFunc(pprof.Cmdline))
    debugGroup.GET("/profile", touka.AdapterStdFunc(pprof.Profile))
    // ... 其他 pprof 路由
}
```

这使得您可以方便地利用 Go 生态中大量现有的、遵循标准接口的第三方中间件和工具。
