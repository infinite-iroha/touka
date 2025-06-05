package touka

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
)

// CreateTestContext 为测试创建一个 *Context 和一个关联的 *Engine。
// 它使用 httptest.NewRecorder() (如果传入的 w 为 nil) 来捕获响应。
// 返回的 Context 已经过初始化，其 Writer 指向提供的 ResponseWriter (或新创建的 Recorder)，
// 其 Request 是一个默认的 "GET /" 请求，并且其 engine 字段指向返回的 Engine 实例。
//
// 参数:
//   - w (http.ResponseWriter): 可选的。如果为 nil，函数内部会创建一个 httptest.ResponseRecorder。
//     通常在测试中，你会传入一个 httptest.ResponseRecorder 来检查响应。
//
// 返回:
//   - c (*Context): 一个初始化的 Touka Context。
//   - r (*Engine): 一个新的 Touka Engine 实例，与 c 相关联。
func CreateTestContext(w http.ResponseWriter) (c *Context, r *Engine) {
	// 1. 如果未提供 ResponseWriter，则创建一个测试用的 Recorder
	// ResponseRecorder 实现了 http.ResponseWriter 接口
	var testResponseWriter http.ResponseWriter = w
	if testResponseWriter == nil {
		testResponseWriter = httptest.NewRecorder()
	}

	// 2. 创建一个新的 Engine 实例
	// 使用 New() 而不是 Default() 以获得一个“干净”的引擎，不带默认中间件 (如 Recovery)
	// 如果你的测试依赖于 Default() 的中间件，可以改为 r = Default()
	r = New()

	// 3. 从 Engine 的池中获取一个 Context 对象
	// 这是模拟真实请求处理的最佳方式
	c = r.pool.Get().(*Context)

	// 4. 创建一个默认的 HTTP 请求
	// 测试时可以根据需要修改这个请求的 Method, URL, Body, Headers 等
	// http.NewRequest 的 body 可以是 nil (对于GET) 或 bytes.NewBufferString("body content") 等
	req, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		// NewRequest 对于 "GET" 和 "/" 以及 nil body 通常不会失败
		// 但作为健壮性考虑，可以 panic 或返回错误
		panic("touka.CreateTestContext: Failed to create dummy request: " + err.Error()) // 英文 panic
	}
	// 确保请求有关联的 Context (尽管 c.reset 也会设置)
	// req = req.WithContext(context.Background()) // 通常 reset 会处理这个

	// 5. 重置/初始化 Context
	// c.reset() 方法期望一个 http.ResponseWriter 和 *http.Request
	// 它会将 c.Writer 包装成 touka.ResponseWriter (responseWriterImpl)
	// 并设置 c.Request, c.Params (清空), c.handlers (nil), c.index (-1),
	// c.Keys (新map), c.Errors (清空), c.ctx (来自 req.Context()),
	// 以及 c.engine (在 c.pool.New 中已经设置，但 reset 会确保其他关联正确)。
	c.reset(testResponseWriter, req)

	// 确保 Context 中的 engine 字段指向我们创建的这个 Engine 实例
	// 虽然 c.pool.New 应该已经做了，但显式确认或设置无害，尤其是如果我们不完全依赖 pool.New 的细节。
	// 在当前的 Context.reset 实现中，c.engine 是在从池中 New() 时由 Engine 自身设置的，
	// reset 方法不会改变它。所以只要 c 是从 r.pool 获取的，c.engine 就应该是 r。

	return c, r
}

// CreateTestContextWithRequest 功能与 CreateTestContext 类似，但允许传入自定义的 *http.Request。
// 这对于测试需要特定请求方法、URL、头部或Body的处理器非常有用。
//
// 参数:
//   - w (http.ResponseWriter): 可选。如果为 nil，创建一个 httptest.ResponseRecorder。
//   - req (*http.Request): 用户提供的 HTTP 请求。如果为 nil，则内部创建一个默认的 "GET /"。
//
// 返回:
//   - c (*Context): 一个初始化的 Touka Context。
//   - r (*Engine): 一个新的 Touka Engine 实例，与 c 相关联。
func CreateTestContextWithRequest(w http.ResponseWriter, req *http.Request) (c *Context, r *Engine) {
	var testResponseWriter http.ResponseWriter = w
	if testResponseWriter == nil {
		testResponseWriter = httptest.NewRecorder()
	}

	r = New()                   // 创建 Engine
	c = r.pool.Get().(*Context) // 从池获取 Context

	var finalReq *http.Request = req
	if finalReq == nil { // 如果未提供请求，创建默认请求
		var err error
		finalReq, err = http.NewRequest(http.MethodGet, "/", nil)
		if err != nil {
			panic("touka.CreateTestContextWithRequest: Failed to create dummy request: " + err.Error()) // 英文 panic
		}
	}

	c.reset(testResponseWriter, finalReq) // 使用提供的或默认的请求重置 Context

	// c.engine 已由 r.pool.New 设置为 r

	return c, r
}

// PerformRequest 在给定的 Engine 上执行一个模拟的 HTTP 请求，并返回响应记录器。
// 这是一个更高级别的测试辅助函数，封装了创建请求、Context 和执行引擎的 ServeHTTP 方法。
//
// 参数:
//   - engine (*Engine): 要测试的 Touka 引擎实例。
//   - method (string): HTTP 请求方法 (例如 "GET", "POST")。
//   - path (string): 请求的路径 (例如 "/", "/users/123?name=test")。
//   - body (io.Reader): 可选的请求体。对于 GET, HEAD 等通常为 nil。
//   - headers (http.Header): 可选的请求头部。
//
// 返回:
//   - *httptest.ResponseRecorder: 包含响应状态、头部和主体的记录器。
//
// 示例:
//
//	rr := touka.PerformRequest(myEngine, "GET", "/ping", nil, nil)
//	assert.Equal(t, http.StatusOK, rr.Code)
//	assert.Equal(t, "pong", rr.Body.String())
func PerformRequest(engine *Engine, method, path string, body io.Reader, headers http.Header) *httptest.ResponseRecorder {
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		// 通常 NewRequest 对于合法的方法和路径不会失败（除非路径解析错误）
		panic(fmt.Sprintf("touka.PerformRequest: Failed to create request %s %s: %v", method, path, err)) // 英文 panic
	}

	// 设置请求头部 (如果提供)
	if headers != nil {
		req.Header = headers
	}

	// 创建一个 ResponseRecorder 来捕获响应
	rr := httptest.NewRecorder()

	// 直接调用 Engine 的 ServeHTTP 方法来处理请求
	// Engine 会负责创建和管理 Context
	engine.ServeHTTP(rr, req)

	return rr
}
