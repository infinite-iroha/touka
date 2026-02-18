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

如果您需要更高级的并发控制（例如从多个异步源接收数据），可以使用通道模式。

```go
r.GET("/events-chan", func(c *touka.Context) {
    eventChan, errChan := c.EventStreamChan()

    // 监听错误/断开连接
    go func() {
        if err := <-errChan; err != nil {
            log.Printf("SSE 错误: %v", err)
        }
    }()

    // 发送数据
    go func() {
        defer close(eventChan) // 务必在结束时关闭

        for i := 0; i < 10; i++ {
            select {
            case <-c.Request.Context().Done():
                return
            default:
                eventChan <- touka.Event{
                    Data: fmt.Sprintf("消息 #%d", i),
                }
                time.Sleep(1 * time.Second)
            }
        }
    }()
})
```

## 最佳实践

1. **资源回收**: 确保在 `EventStreamChan` 模式下正确监听 `c.Request.Context().Done()` 以避免 Goroutine 泄漏。
2. **数据格式**: SSE 协议要求数据为 UTF-8。Touka 的 `Render` 方法会自动处理多行数据并加上必要的 `data:` 前缀。
3. **超时管理**: SSE 连接通常是长连接，请确保您的反向代理（如 Nginx）配置了足够大的写超时时间。

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
