// 文件: touka/adapter.go
package touka

import (
	"net/http"
)

// AdapterStdFunc 将一个标准的 http.HandlerFunc (func(http.ResponseWriter, *http.Request))
// 适配成一个 Touka 框架的 HandlerFunc (func(*Context))
// 这使得标准的 HTTP 处理器可以轻松地在 Touka 路由中使用
//
// 示例:
//
//	stdHandlerFunc := func(w http.ResponseWriter, r *http.Request) {
//	    w.Write([]byte("Hello from a standard handler function!"))
//	}
//	r.GET("/std-func", touka.AdapterStdFunc(stdHandlerFunc))
//
// 注意: 被适配的处理器执行完毕后，Touka 的处理链会被中止 (c.Abort())，
// 因为我们假设标准处理器已经完成了对请求的响应
func AdapterStdFunc(f http.HandlerFunc) HandlerFunc {
	return func(c *Context) {
		// 从 Touka Context 中提取标准的 ResponseWriter 和 Request
		// 并将它们传递给原始的 http.HandlerFunc
		f(c.Writer, c.Request)

		// 中止 Touka 的处理链，防止执行后续的处理器
		c.Abort()
	}
}

// AdapterStdHandle 将一个实现了 http.Handler 接口的对象
// 适配成一个 Touka 框架的 HandlerFunc (func(*Context))
// 这使得像 http.FileServer, http.StripPrefix 或其他第三方库的 Handler
// 可以直接在 Touka 路由中使用
//
// 示例:
//
//	// 创建一个 http.FileServer
//	fileServer := http.FileServer(http.Dir("./static"))
//	// 将 FileServer 适配后用于 Touka 路由
//	r.GET("/static/*filepath", touka.AdapterStdHandle(http.StripPrefix("/static", fileServer)))
//
// 注意: 被适配的处理器执行完毕后，Touka 的处理链会被中止 (c.Abort())
func AdapterStdHandle(h http.Handler) HandlerFunc {
	return func(c *Context) {
		// 调用 Handler 接口的 ServeHTTP 方法
		h.ServeHTTP(c.Writer, c.Request)

		// 中止 Touka 的处理链
		c.Abort()
	}
}
