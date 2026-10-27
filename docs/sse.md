# Server-Sent Events (SSE)

Server-Sent Events 允许服务器向客户端实时推送数据。Touka 对此提供了原生且易用的支持。

## 核心结构：`Event`

`Event` 结构体代表一个 SSE 消息：

```go
type Event struct {
    Event string // 事件名称
    Data  string // 数据内容 (支持多行)
    Id    string // 事件 ID
    Retry string // 重连时间 (毫秒)
}
```

## 模式一：回调模式 (EventStream)

这是最推荐的使用方式，它更简单且能自动管理连接生命周期。

```go
r.GET("/events", func(c *touka.Context) {
    c.EventStream(func(w io.Writer) bool {
        // 构建事件
        event := touka.Event{
            Data: "现在的时间是: " + time.Now().Format(time.RFC3339),
        }

        // 渲染并写入
        if err := event.Render(w); err != nil {
            return false // 发生写入错误（如客户端断开），返回 false 停止流
        }

        time.Sleep(2 * time.Second)
        return true // 返回 true 继续下一次循环
    })
})
```

## 模式二：通道模式 (EventStreamChan)

如果您需要更高级的并发控制（例如从多个异步源接收数据），可以使用通道模式。与回调模式类似，此方法是**阻塞的**：handler 会在此方法中停留，直到事件 channel 被关闭或客户端断开连接。

```go
r.GET("/events-chan", func(c *touka.Context) {
    eventChan := make(chan touka.Event)
    ctx := c.Request.Context()

    // 在独立的 goroutine 中发送事件.
    go func() {
        defer close(eventChan) // 务必在结束时关闭以结束事件流.

        for i := 0; i < 10; i++ {
            select {
            case <-ctx.Done():
                return // 客户端已断开, 退出 goroutine.
            case eventChan <- touka.Event{
                Data: fmt.Sprintf("消息 #%d", i),
            }:
            }
            time.Sleep(1 * time.Second)
        }
    }()

    // EventStreamChan 会阻塞直到流结束.
    c.EventStreamChan(eventChan)
})
```

## 最佳实践

1. **资源回收**: `EventStreamChan` 是阻塞的，handler 在事件流结束前不会返回。将 `c.Request.Context().Done()` 和 `eventChan <- ...` 作为同一个 `select` 的两个分支，确保发送操作本身能够响应客户端断开。事件由 handler goroutine 直接写入，因此 handler 返回后不会再有 goroutine 操作该响应（否则会与 `net/http` 的响应收尾流程争抢 response 的 `bufio.Writer`）。客户端断开（或服务端关闭）时，会用一个已经过去的写超时打断阻塞中的写入；若底层 `ResponseWriter` 不支持写超时（例如自定义包装器未实现 `Unwrap() http.ResponseWriter`），该失败会被记录为警告日志，阻塞中的写入只能等待其自行结束。
2. **关闭 Channel**: 生产者完成发送后必须 `close(eventChan)`，否则 handler 会永远阻塞。
3. **数据格式**: SSE 协议要求数据为 UTF-8。Touka 的 `Render` 方法会自动处理多行数据并加上必要的 `data:` 前缀。
4. **超时管理**: SSE 连接通常是长连接，请确保您的反向代理（如 Nginx）配置了足够大的写超时时间。

## 事件格式与规范符合性

`Event.Render` 的输出遵循 [WHATWG HTML 标准](https://html.spec.whatwg.org/multipage/server-sent-events.html) 中定义的 “Server-sent events” 事件流格式：

- **响应头**：`Content-Type: text/event-stream; charset=utf-8`、状态码 `200`、`Cache-Control: no-cache, no-transform`（`no-transform` 用于阻止代理对事件流做缓冲或压缩）。
- **字段格式**：`name: value`。冒号后的一个前导空格会被客户端剥离，因此 `data: hello` 与 `data:hello` 等价，而数据本身的前导空格会被完整保留。
- **多行数据**：数据按行拆分为多个 `data` 字段，行分隔符支持 `LF`、`CRLF`、`CR`，渲染时统一规范化为 `LF`。客户端会把多个 `data` 字段用 `LF` 重新拼接，因此 `"a\r\nb"` 到达客户端时仍是 `"a\nb"`。
- **事件结束**：每个事件以一个空行结束，客户端收到空行后才会派发该事件；流结束时未收到空行的事件会被丢弃。
- **字段值清洗**：`id`、`event`、`retry` 的值中不允许出现 `CR`/`LF`（`id` 还不允许出现 `NUL`），否则会破坏事件帧结构，甚至让客户端解析出额外的字段。这些字符会被自动移除。
- **`retry` 取值**：只有十进制 ASCII 数字才有意义，其它值会被客户端忽略，因此不会被发送。
- **空数据**：`Data` 为空时不会发送 `data` 字段，客户端不会派发该事件，但 `id` 仍会更新 `Last-Event-ID`（可用于只更新断点位置而不推送数据）。

## 优雅关闭与资源清理

在长连接场景下，正确处理客户端断开或服务器关闭信号至关重要，以防止资源泄漏。

### 示例：监听 Context 取消信号

```go
r.GET("/events-graceful", func(c *touka.Context) {
    // 设置响应头（如果手动处理，EventStream 会自动设置）

    // 使用 Context 的 Done 通道来感知连接关闭
    ctx := c.Request.Context()

    // 启动一个用于模拟数据生成的循环
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()

    c.EventStream(func(w io.Writer) bool {
        select {
        case <-ctx.Done():
            // 收到优雅关闭信号（客户端离开或服务器正在关闭）
            fmt.Println("SSE 连接正在关闭，开始清理资源...")
            return false // 返回 false 告知框架停止流

        case t := <-ticker.C:
            event := touka.Event{
                Data: "Tick at " + t.Format(time.RFC3339),
            }
            if err := event.Render(w); err != nil {
                return false
            }
            return true
        }
    })

    fmt.Println("SSE 连接已彻底释放")
})
```

在该示例中，我们显式地在回调函数中使用 `select` 监听 `ctx.Done()`。虽然 Touka 的 `EventStream` 内部也会检查此信号，但在回调内部自行处理可以执行更复杂的清理逻辑（如关闭数据库连接、停止特定的 Goroutine 等）。

### 为什么会出现 "context deadline exceeded"？

如果您在优雅停机时遇到 `context deadline exceeded` 错误，通常是因为 SSE 连接仍然活跃，而 `http.Server.Shutdown` 正在等待它们结束。

在 Touka 的新版本中，我们通过 `BaseContext` 将 `Engine` 的关闭信号注入到了每个请求的 `Context` 中。这意味着：
1. 当服务器收到关闭信号时，`engine.shutdownCtx` 会被取消。
2. 随后，所有活跃请求的 `c.Request.Context()` 也会收到取消信号。
3. 您的 SSE 处理器中的 `case <-c.Request.Context().Done():` 会立即触发，从而优雅地结束连接。

**注意：** 请务必通过 `r.Run(...)` 并显式传入优雅关闭选项来启动服务器，例如 `touka.WithGracefulShutdown(...)` 或 `touka.WithGracefulShutdownDefault()`。只有启用了优雅关闭，框架才会在服务退出时取消这些请求上下文。
