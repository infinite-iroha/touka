package touka

import (
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
)

// Recovery 返回一个 Touka 的 HandlerFunc，用于捕获处理链中的 panic。
func Recovery() HandlerFunc {
	return func(c *Context) {
		// 使用 defer 和 recover() 来捕获 panic
		defer func() {
			if r := recover(); r != nil {
				// 记录 panic 信息和堆栈追踪
				err := fmt.Errorf("panic occurred: %v", r)
				log.Printf("[Recovery] %s\n%s", err, debug.Stack()) // 记录错误和堆栈

				// 检查客户端是否已断开连接，如果已断开则不再尝试写入响应
				select {
				case <-c.Request.Context().Done():
					log.Printf("[Recovery] Client disconnected, skipping response for panic: %v", r)
					return // 客户端已断开，直接返回
				default:
					// 客户端未断开，返回 500 Internal Server Error
					// 使用统一的错误处理机制
					c.engine.errorHandle.handler(c, http.StatusInternalServerError)
					// Abort() 确保后续的处理函数不再执行
					c.Abort()
				}
			}
		}()

		// 继续执行处理链中的下一个处理函数
		c.Next()
	}
}
