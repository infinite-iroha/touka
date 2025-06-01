package touka

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"

	"github.com/go-json-experiment/json"

	"github.com/WJQSERVER-STUDIO/go-utils/copyb"
	"github.com/WJQSERVER-STUDIO/httpc"
)

const abortIndex int8 = math.MaxInt8 >> 1

// Context 是每个请求的上下文，封装了请求和响应，并提供了很多便捷方法
// 它在中间件和最终处理函数之间传递
type Context struct {
	Writer   ResponseWriter // 包装的 http.ResponseWriter
	Request  *http.Request
	Params   Params        // 从 httprouter 获取的路径参数
	handlers HandlersChain // 当前请求的处理函数链 (中间件 + 最终handler)
	index    int8          // 当前执行到处理链的哪个位置

	mu   sync.RWMutex
	Keys map[string]interface{} // 用于在中间件之间传递数据

	Errors []error // 用于收集处理过程中的错误

	// 缓存查询参数和表单数据
	queryCache url.Values
	formCache  url.Values

	// 携带ctx以实现关闭逻辑
	ctx context.Context

	// HTTPClient 用于在此上下文中执行出站 HTTP 请求。
	// 它由 Engine 提供。
	HTTPClient *httpc.Client

	// 引用所属的 Engine 实例，方便访问 Engine 的配置（如 HTMLRender）
	engine *Engine
}

// --- Context 相关方法实现 ---

// reset 重置 Context 对象以供复用。
// 每次从 sync.Pool 中获取 Context 后，都需要调用此方法进行初始化。
func (c *Context) reset(w http.ResponseWriter, req *http.Request) {
	// 每次重置时，确保 Writer 包装的是最新的 http.ResponseWriter
	// 并重置其内部状态
	if rw, ok := c.Writer.(*responseWriterImpl); ok {
		rw.ResponseWriter = w
		rw.status = 0
		rw.size = 0
	} else {
		// 如果 c.Writer 不是 responseWriterImpl，重新创建
		c.Writer = newResponseWriter(w)
	}

	c.Request = req
	c.Params = c.Params[:0] // 清空 Params 切片，而不是重新分配，以复用底层数组
	c.handlers = nil
	c.index = -1                          // 初始为 -1，`Next()` 将其设置为 0
	c.Keys = make(map[string]interface{}) // 每次请求重新创建 map，避免数据污染
	c.Errors = c.Errors[:0]               // 清空 Errors 切片
	c.queryCache = nil                    // 清空查询参数缓存
	c.formCache = nil                     // 清空表单数据缓存
	c.ctx = req.Context()                 // 使用请求的上下文，继承其取消信号和值
	// c.HTTPClient 和 c.engine 保持不变，它们引用 Engine 实例的成员
}

// Next 在处理链中执行下一个处理函数。
// 这是中间件模式的核心，允许请求依次经过多个处理函数。
func (c *Context) Next() {
	c.index++
	for c.index < int8(len(c.handlers)) {
		c.handlers[c.index](c) // 执行当前索引处的处理函数
		c.index++              // 移动到下一个处理函数
	}
}

// Abort 停止处理链的后续执行。
// 通常在中间件中，当遇到错误或需要提前终止请求时调用。
func (c *Context) Abort() {
	c.index = abortIndex // 将 index 设置为一个很大的值，使后续 Next() 调用跳过所有处理函数
}

// IsAborted 返回处理链是否已被中止。
func (c *Context) IsAborted() bool {
	return c.index >= abortIndex
}

// AbortWithStatus 中止处理链并设置 HTTP 状态码。
func (c *Context) AbortWithStatus(code int) {
	c.Writer.WriteHeader(code) // 设置响应状态码
	c.Abort()                  // 中止处理链
}

// Set 将一个键值对存储到 Context 中。
// 这是一个线程安全的操作，用于在中间件之间传递数据。
func (c *Context) Set(key string, value interface{}) {
	c.mu.Lock() // 加写锁
	if c.Keys == nil {
		c.Keys = make(map[string]interface{})
	}
	c.Keys[key] = value
	c.mu.Unlock() // 解写锁
}

// Get 从 Context 中获取一个值。
// 这是一个线程安全的操作。
func (c *Context) Get(key string) (value interface{}, exists bool) {
	c.mu.RLock() // 加读锁
	value, exists = c.Keys[key]
	c.mu.RUnlock() // 解读锁
	return
}

// MustGet 从 Context 中获取一个值，如果不存在则 panic。
// 适用于确定值一定存在的场景。
func (c *Context) MustGet(key string) interface{} {
	if value, exists := c.Get(key); exists {
		return value
	}
	panic("Key \"" + key + "\" does not exist in context.")
}

// Query 从 URL 查询参数中获取值。
// 懒加载解析查询参数，并进行缓存。
func (c *Context) Query(key string) string {
	if c.queryCache == nil {
		c.queryCache = c.Request.URL.Query() // 首次访问时解析并缓存
	}
	return c.queryCache.Get(key)
}

// DefaultQuery 从 URL 查询参数中获取值，如果不存在则返回默认值。
func (c *Context) DefaultQuery(key, defaultValue string) string {
	if value := c.Query(key); value != "" {
		return value
	}
	return defaultValue
}

// PostForm 从 POST 请求体中获取表单值。
// 懒加载解析表单数据，并进行缓存。
func (c *Context) PostForm(key string) string {
	if c.formCache == nil {
		c.Request.ParseMultipartForm(defaultMemory) // 解析 multipart/form-data 或 application/x-www-form-urlencoded
		c.formCache = c.Request.PostForm
	}
	return c.formCache.Get(key)
}

// DefaultPostForm 从 POST 请求体中获取表单值，如果不存在则返回默认值。
func (c *Context) DefaultPostForm(key, defaultValue string) string {
	if value := c.PostForm(key); value != "" {
		return value
	}
	return defaultValue
}

// Param 从 URL 路径参数中获取值。
// 例如，对于路由 /users/:id，c.Param("id") 可以获取 id 的值。
func (c *Context) Param(key string) string {
	return c.Params.ByName(key)
}

// Raw 向响应写入bytes
func (c *Context) Raw(code int, contentType string, data []byte) {
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.WriteHeader(code)
	c.Writer.Write(data)
}

// String 向响应写入格式化的字符串。
func (c *Context) String(code int, format string, values ...interface{}) {
	c.Writer.WriteHeader(code)
	c.Writer.Write([]byte(fmt.Sprintf(format, values...)))
}

// JSON 向响应写入 JSON 数据。
// 设置 Content-Type 为 application/json。
func (c *Context) JSON(code int, obj interface{}) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(code)
	// 实际 JSON 编码
	jsonBytes, err := json.Marshal(obj)
	if err != nil {
		c.AddError(fmt.Errorf("failed to marshal JSON: %w", err))
		c.String(http.StatusInternalServerError, "Internal Server Error: Failed to marshal JSON")
		return
	}
	c.Writer.Write(jsonBytes)
}

// HTML 渲染 HTML 模板。
// 如果 Engine 配置了 HTMLRender，则使用它进行渲染。
// 否则，会进行简单的字符串输出。
// 预留接口，可以扩展为支持多种模板引擎。
func (c *Context) HTML(code int, name string, obj interface{}) {
	c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Writer.WriteHeader(code)

	if c.engine != nil && c.engine.HTMLRender != nil {
		// 假设 HTMLRender 是一个 *template.Template 实例
		if tpl, ok := c.engine.HTMLRender.(*template.Template); ok {
			err := tpl.ExecuteTemplate(c.Writer, name, obj)
			if err != nil {
				c.AddError(fmt.Errorf("failed to render HTML template '%s': %w", name, err))
				c.String(http.StatusInternalServerError, "Internal Server Error: Failed to render HTML template")
			}
			return
		}
		// 可以扩展支持其他渲染器接口
	}
	// 默认简单输出，用于未配置 HTMLRender 的情况
	c.Writer.Write([]byte(fmt.Sprintf("<!-- HTML rendered for %s -->\n<pre>%v</pre>", name, obj)))
}

// Redirect 执行 HTTP 重定向。
// code 应为 3xx 状态码 (如 http.StatusMovedPermanently, http.StatusFound)。
func (c *Context) Redirect(code int, location string) {
	http.Redirect(c.Writer, c.Request, location, code)
	c.Abort()
	if fl, ok := c.Writer.(http.Flusher); ok {
		fl.Flush()
	}
}

// ShouldBindJSON 尝试将请求体绑定到 JSON 对象。
func (c *Context) ShouldBindJSON(obj interface{}) error {
	if c.Request.Body == nil {
		return errors.New("request body is empty")
	}
	/*
		decoder := json.NewDecoder(c.Request.Body)
		if err := decoder.Decode(obj); err != nil {
			return fmt.Errorf("json binding error: %w", err)
		}
	*/
	err := json.UnmarshalRead(c.Request.Body, obj)
	if err != nil {
		return fmt.Errorf("json binding error: %w", err)
	}
	return nil
}

// ShouldBind 尝试将请求体绑定到各种类型（JSON, Form, XML 等）。
// 这是一个复杂的通用绑定接口，通常根据 Content-Type 或其他头部来判断绑定方式。
// 预留接口，可根据项目需求进行扩展。
func (c *Context) ShouldBind(obj interface{}) error {
	// TODO: 完整的通用绑定逻辑
	// 可以根据 c.Request.Header.Get("Content-Type") 来判断是 JSON, Form, XML 等
	// 例如：
	// contentType := c.Request.Header.Get("Content-Type")
	// if strings.HasPrefix(contentType, "application/json") {
	//     return c.ShouldBindJSON(obj)
	// }
	// if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") || strings.HasPrefix(contentType, "multipart/form-data") {
	//     return c.ShouldBindForm(obj) // 需要实现 ShouldBindForm
	// }
	return errors.New("generic binding not fully implemented yet, implement based on Content-Type")
}

// AddError 添加一个错误到 Context。
// 允许在处理请求过程中收集多个错误。
func (c *Context) AddError(err error) {
	c.Errors = append(c.Errors, err)
}

// Errors 返回 Context 中收集的所有错误。
func (c *Context) GetErrors() []error {
	return c.Errors
}

// Client 返回 Engine 提供的 HTTPClient。
// 方便在请求处理函数中进行出站 HTTP 请求。
func (c *Context) Client() *httpc.Client {
	return c.HTTPClient
}

// Context() 返回请求的上下文，用于取消操作。
// 这是 Go 标准库的 `context.Context`，用于请求的取消和超时管理。
func (c *Context) Context() context.Context {
	return c.ctx
}

// Done returns a channel that is closed when the request context is cancelled or times out.
// 继承自 `context.Context`。
func (c *Context) Done() <-chan struct{} {
	return c.ctx.Done()
}

// Err returns the error, if any, that caused the context to be canceled or to
// time out.
// 继承自 `context.Context`。
func (c *Context) Err() error {
	return c.ctx.Err()
}

// Value returns the value associated with this context for key, or nil if no
// value is associated with key.
// 可以用于从 Context 中获取与特定键关联的值，包括 Go 原生 Context 的值和 Touka Context 的 Keys。
func (c *Context) Value(key interface{}) interface{} {
	if keyAsString, ok := key.(string); ok {
		if val, exists := c.Get(keyAsString); exists {
			return val
		}
	}
	return c.ctx.Value(key) // 尝试从 Go 原生 Context 中获取值
}

// GetWriter 获得一个 io.Writer 接口，可以直接向响应体写入数据。
// 这对于需要自定义流式写入或与其他需要 io.Writer 的库集成非常有用。
func (c *Context) GetWriter() io.Writer {
	return c.Writer // ResponseWriter 接口嵌入了 http.ResponseWriter，而 http.ResponseWriter 实现了 io.Writer
}

// WriteStream 接受一个 io.Reader 并将其内容流式传输到响应体。
// 返回写入的字节数和可能遇到的错误。
// 该方法在开始写入之前，会确保设置 HTTP 状态码为 200 OK。
func (c *Context) WriteStream(reader io.Reader) (written int64, err error) {
	// 确保在写入数据前设置状态码。
	// WriteHeader 会在第一次写入时被 Write 方法隐式调用，但显式调用可以确保状态码的预期。
	if !c.Writer.Written() {
		c.Writer.WriteHeader(http.StatusOK) // 默认 200 OK
	}

	written, err = copyb.Copy(c.Writer, reader) // 从 reader 读取并写入 ResponseWriter
	if err != nil {
		c.AddError(fmt.Errorf("failed to write stream: %w", err))
	}
	return written, err
}

// GetReqBody 以获取一个 io.ReadCloser 接口，用于读取请求体
// 注意：请求体只能读取一次。
func (c *Context) GetReqBody() io.ReadCloser {
	return c.Request.Body
}

// GetReqBodyFull
// GetReqBodyFull 读取并返回请求体的所有内容。
// 注意：请求体只能读取一次。
func (c *Context) GetReqBodyFull() ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	defer c.Request.Body.Close() // 确保请求体被关闭
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.AddError(fmt.Errorf("failed to read request body: %w", err))
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	return data, nil
}

// RequestIP 返回客户端的 IP 地址。
// 它会根据 Engine 的配置 (ForwardByClientIP) 尝试从 X-Forwarded-For 或 X-Real-IP 等头部获取，
// 否则回退到 Request.RemoteAddr。
func (c *Context) RequestIP() string {
	if c.engine.ForwardByClientIP {
		for _, headerName := range c.engine.RemoteIPHeaders {
			if ipValue := c.Request.Header.Get(headerName); ipValue != "" {
				// X-Forwarded-For 可能包含多个 IP，约定第一个（最左边）是客户端 IP
				// 其他头部（如 X-Real-IP）通常只有一个
				ips := strings.Split(ipValue, ",")
				for _, singleIP := range ips {
					trimmedIP := strings.TrimSpace(singleIP)
					// 使用 netip.ParseAddr 进行 IP 地址的解析和格式验证
					addr, err := netip.ParseAddr(trimmedIP)
					if err == nil {
						// 成功解析到合法的 IP 地址格式，立即返回
						return addr.String()
					}
					// 如果当前 singleIP 无效，继续检查列表中的下一个
				}
			}
		}
	}

	// 如果没有启用 ForwardByClientIP 或头部中没有找到有效 IP，回退到 Request.RemoteAddr
	// RemoteAddr 通常是 "host:port" 格式，但也可能直接就是 IP 地址
	remoteAddrStr := c.Request.RemoteAddr
	ip, _, err := net.SplitHostPort(remoteAddrStr) // 尝试分离 host 和 port
	if err != nil {
		// 如果分离失败，意味着 remoteAddrStr 可能直接就是 IP 地址（或畸形）
		ip = remoteAddrStr // 此时将整个 remoteAddrStr 作为候选 IP
	}

	// 对从 RemoteAddr 中提取/使用的 IP 进行最终的合法性验证
	addr, parseErr := netip.ParseAddr(ip)
	if parseErr == nil {
		return addr.String() // 成功解析并返回合法 IP
	}

	return ""
}

// ClientIP 返回客户端的 IP 地址。
// 这是一个别名，与 RequestIP 功能相同。
func (c *Context) ClientIP() string {
	return c.RequestIP()
}

// ContentType 返回请求的 Content-Type 头部。
func (c *Context) ContentType() string {
	return c.GetReqHeader("Content-Type")
}

// UserAgent 返回请求的 User-Agent 头部。
func (c *Context) UserAgent() string {
	return c.GetReqHeader("User-Agent")
}

// Status 设置响应状态码。
func (c *Context) Status(code int) {
	c.Writer.WriteHeader(code)
}

// File 将指定路径的文件作为响应发送。
// 它会设置 Content-Type 和 Content-Disposition 头部。
func (c *Context) File(filepath string) {
	http.ServeFile(c.Writer, c.Request, filepath)
	c.Abort() // 发送文件后中止后续处理
}

// SetHeader 设置响应头部。
func (c *Context) SetHeader(key, value string) {
	c.Writer.Header().Set(key, value)
}

// AddHeader 添加响应头部。
func (c *Context) AddHeader(key, value string) {
	c.Writer.Header().Add(key, value)
}

// DelHeader 删除响应头部。
func (c *Context) DelHeader(key string) {
	c.Writer.Header().Del(key)
}

// GetReqHeader 获取请求头部的值。
func (c *Context) GetReqHeader(key string) string {
	return c.Request.Header.Get(key)
}

// GetAllReqHeader 获取所有请求头部。
func (c *Context) GetAllReqHeader() http.Header {
	return c.Request.Header
}

// 使用定义的errorHandle来处理error并结束当前handle
func (c *Context) ErrorUseHandle(code int) {
	if c.engine != nil && c.engine.errorHandle.handler != nil {
		c.engine.errorHandle.handler(c, code)
		c.Abort()
		return
	} else {
		// Default error handling if no custom handler is set
		c.String(code, http.StatusText(code))
		c.Abort()
	}
}

// GetProtocol 获取当前连接版本
func (c *Context) GetProtocol() string {
	return c.Request.Proto
}
