# 路由系统

Touka 拥有一个强大且灵活的路由系统，底层基于高性能的基数树（Radix Tree）实现。

## 基础路由

您可以为所有标准的 HTTP 方法注册处理器：

```go
r.GET("/someGet", handle)
r.POST("/somePost", handle)
r.PUT("/somePut", handle)
r.DELETE("/someDelete", handle)
r.PATCH("/somePatch", handle)
r.HEAD("/someHead", handle)
r.OPTIONS("/someOptions", handle)

// 注册所有上述方法的路由
r.ANY("/any", handle)
```

## 路径参数 (Named Parameters)

使用冒号 `:` 定义路径参数。参数值可以通过 `c.Param(key)` 获取。

```go
// 匹配 /user/john, 不匹配 /user/ 或 /user/john/send
r.GET("/user/:name", func(c *touka.Context) {
    name := c.Param("name")
    c.String(http.StatusOK, "Hello %s", name)
})

// 匹配 /user/john/send
r.GET("/user/:name/:action", func(c *touka.Context) {
    name := c.Param("name")
    action := c.Param("action")
    c.String(http.StatusOK, "%s is doing %s", name, action)
})
```

## 通配符路由 (Catch-all Parameters)

使用星号 `*` 定义通配符路由，它会捕获路径中该位置之后的所有内容。

```go
// 匹配 /src/main.go, /src/scripts/app.js 等
r.GET("/src/*filepath", func(c *touka.Context) {
    path := c.Param("filepath")
    c.String(http.StatusOK, "Viewing file: %s", path)
})
```

## 路由组 (RouterGroup)

路由组允许您共享公共路径前缀或中间件，使代码结构更清晰。

```go
v1 := r.Group("/api/v1")
{
    v1.GET("/login", loginEndpoint)
    v1.GET("/submit", submitEndpoint)
}

v2 := r.Group("/api/v2")
v2.Use(AuthMiddleware()) // 仅应用于 v2 组
{
    v2.POST("/data", dataEndpoint)
}
```

## 路由行为配置

Touka 允许您自定义路由匹配的行为：

- **RedirectTrailingSlash**: 如果启用（默认），请求 `/foo/` 会被重定向到 `/foo`（如果只有后者注册了），反之亦然。
- **RedirectFixedPath**: 如果启用（默认），引擎会尝试修复路径大小写或移除多余的斜杠并重定向。
- **HandleMethodNotAllowed**: 如果启用，当请求路径匹配但方法不匹配时，返回 405 而非 404。

```go
r := touka.New()
r.RedirectTrailingSlash = true
r.HandleMethodNotAllowed = true
```

## 获取已注册路由信息

您可以使用 `GetRouterInfo` 获取当前引擎中所有已注册路由的列表。

```go
routes := r.GetRouterInfo()
for _, route := range routes {
    fmt.Printf("Method: %s, Path: %s\n", route.Method, route.Path)
}
```
