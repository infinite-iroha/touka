# 统一错误处理

Touka 的核心优势之一是其**高度统一且自动化**的错误处理机制。

## 全局错误处理器

您可以为整个引擎设置一个统一的错误处理器。无论错误来自您的业务代码，还是来自框架内部（如 404/405），甚至是来自标准库的 `http.FileServer`，最终都会流向这个处理器。

```go
r.SetErrorHandler(func(c *touka.Context, code int, err error) {
    // 您可以在这里定义统一的错误响应格式
    c.JSON(code, touka.H{
        "code":    code,
        "message": http.StatusText(code),
        "detail":  err.Error(),
    })

    // 也可以记录日志
    c.Errorf("HTTP Error %d: %v", code, err)
})
```

## `errorCapturingResponseWriter` (ecw) 的工作原理

很多时候，我们希望拦截标准库组件（如 `http.FileServer`）产生的错误，以便能够应用我们自定义的 404 页面或 JSON 响应。

Touka 通过包装标准的 `http.ResponseWriter` 实现了这一点：

1. **拦截写入**: 当 `http.FileServer` 等组件尝试调用 `WriteHeader(statusCode)` 且 `statusCode >= 400` 时，Touka 的包装器会捕获这个状态码。
2. **阻止输出**: 它会阻止组件继续向响应体写入默认的错误消息（如 `404 page not found`）。
3. **回调处理**: 包装器随后会调用全局配置的 `ErrorHandler`。

这意味着您可以像这样轻松地为静态文件服务设置自定义错误处理：

```go
r := touka.New()

// 设置全局错误处理
r.SetErrorHandler(func(c *touka.Context, code int, err error) {
    if code == http.StatusNotFound {
        c.String(http.StatusNotFound, "找不到此资源")
        return
    }
    c.String(code, "发生错误: %v", err)
})

// 服务静态目录
r.StaticDir("/static", "./public")
// 如果用户访问 /static/missing-file.jpg，他将看到 "找不到此资源"
```

## 手动触发错误处理

您也可以在处理器中通过 `c.ErrorUseHandle` 手动触发此流程：

```go
r.GET("/item/:id", func(c *touka.Context) {
    item, err := db.GetItem(c.Param("id"))
    if err != nil {
        // 调用全局错误处理器
        c.ErrorUseHandle(http.StatusInternalServerError, err)
        return
    }
    c.JSON(http.StatusOK, item)
})
```
