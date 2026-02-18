# 高级特性与优化

本章节涵盖了 Touka 的一些深层特性以及在生产环境中的最佳实践。

## 性能优化

### 1. Context 池化

Touka 使用 `sync.Pool` 来重用 `touka.Context` 对象。这极大减少了每个请求产生的内存分配和 GC 压力。
- **代价**: 您必须在处理器返回后立即停止对该 `Context` 指针的任何引用。
- **解决方案**: 如果需要在后台 Goroutine 中使用请求数据，请预先提取所需数据（如 `c.Query` 的值），或者深拷贝该对象（不推荐）。

### 2. 预分配参数切片

在路由匹配过程中，Touka 会预分配路径参数切片，并根据路由深度进行缓存，从而在路由查找时实现几乎零分配。

## 优雅停机 (Graceful Shutdown)

在部署新版本时，我们希望服务器停止接收新请求，但能处理完当前正在进行的请求。

```go
r := touka.Default()
// ... 注册路由 ...

// 监听 SIGINT 和 SIGTERM 信号
// 如果在 10 秒内未处理完，则强制关闭
if err := r.RunShutdown(":8080", 10*time.Second); err != nil {
    log.Fatal("服务器退出异常:", err)
}
```

## 与标准库集成

Touka 遵循 `net/http` 哲学。您可以方便地使用现有的标准库组件。

### 适配 `http.HandlerFunc`

```go
r.GET("/pprof/*any", touka.AdapterStdFunc(pprof.Index))
```

### 手动注入

由于 `Engine` 实现了 `http.Handler` 接口，您可以将其挂载到任何地方。

```go
s := &http.Server{
    Addr:           ":8080",
    Handler:        r, // Engine 实例
    ReadTimeout:    10 * time.Second,
    WriteTimeout:   10 * time.Second,
    MaxHeaderBytes: 1 << 20,
}
s.ListenAndServe()
```

## 自定义日志集成

Touka 默认集成了 `reco` 日志库。您可以自定义其输出行为。

```go
logConfig := reco.Config{
    Level:  reco.LevelInfo,
    Output: os.Stdout,
    Async:  true, // 异步写入提高性能
}
r.SetLoggerCfg(logConfig)
```

## 内存读取限制 (MaxReader)

为了防止恶意的大数据包攻击（如慢速 HTTP 攻击或内存溢出），Touka 内置了 `MaxReader` 机制。

```go
// 设置全局最大读取限制（例如 2MB）
r.SetMaxReader(2 << 20)
```
