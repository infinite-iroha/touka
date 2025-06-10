// 文件: touka/recovery.go
package touka

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil" // 用于 DumpRequest
	"os"
	"runtime/debug"
	"strings"
)

// PanicHandlerFunc 定义了用户自定义的 panic 处理函数类型
// 它接收当前的 Context 和 panic 的值
type PanicHandlerFunc func(c *Context, panicInfo interface{})

// RecoveryWithOptions 返回一个可配置的 panic 恢复中间件
//
// 参数:
//   - handler (PanicHandlerFunc): 一个可选的回调函数 如果提供了，当 panic 发生时，
//     它将被调用，允许用户进行自定义的日志记录、错误上报或响应
//     如果为 nil，将使用默认的 panic 处理逻辑
func RecoveryWithOptions(handler PanicHandlerFunc) HandlerFunc {
	// 如果未提供 handler，则使用默认的 panic 处理器
	if handler == nil {
		handler = defaultPanicHandler
	}

	return func(c *Context) {
		defer func() {
			if r := recover(); r != nil {
				// 捕获到 panic，调用配置的处理器
				handler(c, r)
			}
		}()
		c.Next() // 执行后续的处理链
	}
}

// Recovery 返回一个使用默认配置的 panic 恢复中间件
// 它是 RecoveryWithOptions(nil) 的一个便捷包装
func Recovery() HandlerFunc {
	return RecoveryWithOptions(nil) // 使用默认处理器
}

// defaultPanicHandler 是默认的 panic 处理逻辑
func defaultPanicHandler(c *Context, r interface{}) {
	// 检查连接是否已由客户端关闭
	// 常见的错误类型包括 net.OpError (其内部错误可能是 os.SyscallError)，
	// 以及在 HTTP/2 中可能出现的特定 stream 错误
	// isBrokenPipeError 是一个辅助函数，用于检查这些情况
	if isBrokenPipeError(r) {
		// 如果是客户端断开连接导致的 panic，我们不应再尝试写入响应
		// 只需要记录一个信息级别的日志，然后中止处理
		log.Printf("[Recovery] Client connection closed for request %s %s. Panic: %v. No response sent.",
			c.Request.Method, c.Request.URL.Path, r)
		c.Abort() // 仅设置中止标志
		return
	}

	// 对于其他类型的 panic，我们认为是服务器端内部错误
	// 记录详细的错误日志，包括请求信息和堆栈跟踪
	// 使用 httputil.DumpRequest 来获取请求的快照，但注意不要读取 Body
	httpRequest, _ := httputil.DumpRequest(c.Request, false)
	// 隐藏敏感头部信息，例如 Authorization
	headers := strings.Split(string(httpRequest), "\r\n")
	for idx, header := range headers {
		current := strings.SplitN(header, ":", 2)
		if len(current) > 1 && strings.EqualFold(current[0], "Authorization") {
			headers[idx] = current[0] + ": [REDACTED]" // 替换为脱敏信息
		}
	}
	redactedRequest := strings.Join(headers, "\r\n")
	// 使用英文记录日志
	log.Printf("[Recovery] Panic recovered:\nPanic: %v\nRequest:\n%s\nStack:\n%s",
		r, redactedRequest, string(debug.Stack()))

	// 在发送 500 错误响应之前，检查响应是否已经开始写入
	// 如果 c.Writer.Written() 返回 true，说明响应头已经发送，
	// 此时再尝试写入状态码或响应体会导致错误或 panic，所以应该直接中止
	if c.Writer.Written() {
		// 使用英文记录日志
		log.Println("[Recovery] Response headers already sent. Cannot write 500 error.")
		c.Abort()
		return
	}

	// 尝试发送 500 Internal Server Error 响应
	// 使用框架提供的统一错误处理器（如果可用）
	if c.engine != nil && c.engine.errorHandle.handler != nil {
		c.engine.errorHandle.handler(c, http.StatusInternalServerError, errors.New("Internal Panic Error"))
	} else {
		// 如果框架错误处理器不可用，提供一个备用的简单响应
		// 返回英文错误信息
		http.Error(c.Writer, "Internal Server Error", http.StatusInternalServerError)
	}
	// 确保 Touka 的处理链被中止
	// errorHandle.handler 通常会调用 Abort，但在这里再次调用是安全的
	c.Abort()
}

// isBrokenPipeError 检查 recover() 捕获的值是否表示一个由客户端断开连接引起的网络错误
// 这对于防止在已关闭的连接上写入响应至关重要
func isBrokenPipeError(r interface{}) bool {
	// 将 recover() 的结果转换为 error 类型
	err, ok := r.(error)
	if !ok {
		return false // 如果 panic 的不是一个 error，则不认为是 broken pipe
	}

	var opErr *net.OpError
	// 检查错误链中是否存在 net.OpError
	if errors.As(err, &opErr) {
		var syscallErr *os.SyscallError
		// 检查 net.OpError 的内部错误是否是 os.SyscallError
		if errors.As(opErr.Err, &syscallErr) {
			// 将系统调用错误转换为小写字符串进行检查
			errMsg := strings.ToLower(syscallErr.Error())
			// 常见的由客户端断开引起的错误消息
			if strings.Contains(errMsg, "broken pipe") || strings.Contains(errMsg, "connection reset by peer") {
				return true
			}
		}
	}

	// 还需要处理 HTTP/2 中的 stream closed 错误
	// 在 Go 1.16+ 中，当写入已关闭的 HTTP/2 流时，可能会返回 io.EOF
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		// 在流式写入的上下文中，io.EOF 或 net.ErrClosed 也常常表示连接已关闭
		return true
	}

	if errors.Is(err, http.ErrAbortHandler) {
		return true
	}

	return false
}
