package touka

import (
	"errors"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

// WebSocketHandler 是用户提供的用于处理 WebSocket 连接的函数类型。
// conn 是一个已经完成握手的 WebSocket 连接。
type WebSocketHandler func(c *Context, conn *websocket.Conn)

// WebSocketUpgradeOptions 用于配置 WebSocket 升级中间件。
type WebSocketUpgradeOptions struct {
	// Upgrader 是 gorilla/websocket.Upgrader 的实例。
	// 用户可以配置 ReadBufferSize, WriteBufferSize, CheckOrigin, Subprotocols 等。
	// 如果为 nil，将使用一个带有合理默认值的 Upgrader。
	Upgrader *websocket.Upgrader

	// Handler 是在 WebSocket 成功升级后调用的处理函数。
	// 这个字段是必需的。
	Handler WebSocketHandler

	// OnError 是一个可选的回调函数，用于处理升级过程中发生的错误。
	// 如果未提供，错误将导致一个标准的 HTTP 错误响应（例如 400 Bad Request）。
	OnError func(c *Context, status int, err error)
}

// defaultWebSocketUpgrader 返回一个具有合理默认值的 websocket.Upgrader。
func defaultWebSocketUpgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		// CheckOrigin 应该由用户根据其安全需求来配置。
		// 默认情况下，如果 Origin 头部存在且与 Host 头部不匹配，会拒绝连接。
		// 对于开发，可以暂时设置为 func(r *http.Request) bool { return true }
		// 但在生产环境中必须小心配置。
		CheckOrigin: func(r *http.Request) bool {
			// 简单的同源检查或允许所有 (根据需要调整)
			// return r.Header.Get("Origin") == "" || strings.HasPrefix(r.Header.Get("Origin"), "http://"+r.Host) || strings.HasPrefix(r.Header.Get("Origin"), "https://"+r.Host)
			return true // 示例：允许所有，生产环境请谨慎
		},
	}
}

// defaultWebSocketOnError 是默认的错误处理函数。
func defaultWebSocketOnError(c *Context, status int, err error) {
	// 使用框架的错误处理机制或简单的字符串响应
	// 确保不要写入一个已经开始的响应
	if !c.Writer.Written() {
		// 返回英文错误信息
		errMsg := http.StatusText(status)
		if err != nil {
			errMsg = err.Error() // 可以考虑是否暴露详细错误
		}
		c.String(status, "%s", errMsg) // 或者 c.engine.errorHandle.handler(c, status)
	}
	c.Abort() // 总是中止
}

// WebSocketUpgrade 返回一个 WebSocket 升级中间件。
// 它能自动感知 HTTP/1.1 的 Upgrade 请求和 HTTP/2 的扩展 CONNECT 请求 (RFC 8441)。
func WebSocketUpgrade(opts WebSocketUpgradeOptions) HandlerFunc {
	if opts.Handler == nil {
		panic("touka: WebSocketUpgradeOptions.Handler cannot be nil")
	}

	upgrader := opts.Upgrader
	if upgrader == nil {
		upgrader = defaultWebSocketUpgrader()
	}

	onError := opts.OnError
	if onError == nil {
		onError = defaultWebSocketOnError
	}

	return func(c *Context) {
		// 调试日志，查看请求详情
		// reqBytes, _ := httputil.DumpRequest(c.Request, true)
		// log.Printf("WebSocketUpgrade: Incoming request for path %s:\n%s", c.Request.URL.Path, string(reqBytes))
		// log.Printf("Request Proto: %s, Method: %s", c.Request.Proto, c.Request.Method)

		// 对于我们的目的，让 gorilla/websocket 的 Upgrade 方法去判断更佳，
		// 它已经实现了 RFC 8441 的支持。

		// 我们不再需要手动区分 HTTP/1.1 和 HTTP/2 的逻辑，
		// gorilla/websocket.Upgrader.Upgrade 会自动处理。
		// 它会检查请求是 HTTP/1.1 Upgrade 还是 HTTP/2 CONNECT with :protocol=websocket。

		// 对于 HTTP/2，Upgrade() 方法不会发送 101，而是处理 CONNECT 的 200 OK。
		// 它也不会调用 Hijack，因为连接已经在 HTTP/2 流上。
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			// 升级失败。gorilla/websocket.Upgrade 会处理错误响应的发送。
			// (对于 HTTP/1.1 会是 400/403 等；对于 HTTP/2 也是类似的非 2xx 响应)
			var httpErr websocket.HandshakeError
			statusCode := http.StatusBadRequest // 默认
			if errors.As(err, &httpErr) {
				// 尝试获取更具体的错误信息，但状态码可能不直接暴露
			}

			// 使用英文记录日志
			log.Printf("WebSocket upgrade/handshake failed for %s (Proto: %s): %v", c.Request.RemoteAddr, c.Request.Proto, err)
			onError(c, statusCode, err)
			if !c.IsAborted() {
				c.Abort()
			}
			return
		}

		// 升级/握手成功
		// 使用英文记录日志
		log.Printf("WebSocket connection established for %s (Proto: %s)", c.Request.RemoteAddr, c.Request.Proto)

		if !c.IsAborted() {
			c.Abort() // 确保 HTTP 处理链中止
		}

		defer func() {
			// 使用英文记录日志
			log.Printf("Closing WebSocket connection for %s", conn.RemoteAddr())
			_ = conn.Close()
		}()

		opts.Handler(c, conn) // 执行用户定义的 WebSocket 逻辑
	}
}
