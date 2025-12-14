// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/WJQSERVER/wanf"
	"github.com/fenthope/reco"
	"github.com/go-json-experiment/json"

	"github.com/WJQSERVER-STUDIO/go-utils/iox"
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
	Keys map[string]any // 用于在中间件之间传递数据

	Errors []error // 用于收集处理过程中的错误

	// 缓存查询参数和表单数据
	queryCache url.Values
	formCache  url.Values

	// 携带ctx以实现关闭逻辑
	ctx context.Context

	// HTTPClient 用于在此上下文中执行出站 HTTP 请求
	// 它由 Engine 提供
	HTTPClient *httpc.Client

	// 引用所属的 Engine 实例，方便访问 Engine 的配置（如 HTMLRender）
	engine *Engine

	sameSite http.SameSite

	// 请求体Body大小限制
	MaxRequestBodySize int64

	// skippedNodes 用于记录跳过的节点信息，以便回溯
	// 通常在处理嵌套路由时使用
	SkippedNodes []skippedNode
}

// --- Context 相关方法实现 ---

// reset 重置 Context 对象以供复用
// 每次从 sync.Pool 中获取 Context 后，都需要调用此方法进行初始化
func (c *Context) reset(w http.ResponseWriter, req *http.Request) {

	if rw, ok := c.Writer.(*responseWriterImpl); ok && !rw.IsHijacked() {
		rw.reset(w)
	} else {
		c.Writer = newResponseWriter(w)
	}

	c.Request = req
	//c.Params = c.Params[:0] // 清空 Params 切片，而不是重新分配，以复用底层数组
	//避免params长度为0
	if cap(c.Params) > 0 {
		c.Params = c.Params[:0]
	} else {
		c.Params = make(Params, 0, c.engine.maxParams)
	}
	c.handlers = nil
	c.index = -1                          // 初始为 -1，`Next()` 将其设置为 0
	c.Keys = make(map[string]any)         // 每次请求重新创建 map，避免数据污染
	c.Errors = c.Errors[:0]               // 清空 Errors 切片
	c.queryCache = nil                    // 清空查询参数缓存
	c.formCache = nil                     // 清空表单数据缓存
	c.ctx = req.Context()                 // 使用请求的上下文，继承其取消信号和值
	c.sameSite = http.SameSiteDefaultMode // 默认 SameSite 模式
	c.MaxRequestBodySize = c.engine.GlobalMaxRequestBodySize

	if cap(c.SkippedNodes) > 0 {
		c.SkippedNodes = c.SkippedNodes[:0]
	} else {
		c.SkippedNodes = make([]skippedNode, 0, 256)
	}
}

// Next 在处理链中执行下一个处理函数
// 这是中间件模式的核心，允许请求依次经过多个处理函数
func (c *Context) Next() {
	c.index++
	for c.index < int8(len(c.handlers)) {
		c.handlers[c.index](c) // 执行当前索引处的处理函数
		c.index++              // 移动到下一个处理函数
	}
}

// Abort 停止处理链的后续执行
// 通常在中间件中，当遇到错误或需要提前终止请求时调用
func (c *Context) Abort() {
	c.index = abortIndex // 将 index 设置为一个很大的值，使后续 Next() 调用跳过所有处理函数
}

// IsAborted 返回处理链是否已被中止
func (c *Context) IsAborted() bool {
	return c.index >= abortIndex
}

// AbortWithStatus 中止处理链并设置 HTTP 状态码
func (c *Context) AbortWithStatus(code int) {
	c.Writer.WriteHeader(code) // 设置响应状态码
	c.Abort()                  // 中止处理链
}

// Set 将一个键值对存储到 Context 中
// 这是一个线程安全的操作，用于在中间件之间传递数据
func (c *Context) Set(key string, value any) {
	c.mu.Lock() // 加写锁
	if c.Keys == nil {
		c.Keys = make(map[string]any)
	}
	c.Keys[key] = value
	c.mu.Unlock() // 解写锁
}

// Get 从 Context 中获取一个值
// 这是一个线程安全的操作
func (c *Context) Get(key string) (value any, exists bool) {
	c.mu.RLock() // 加读锁
	value, exists = c.Keys[key]
	c.mu.RUnlock() // 解读锁
	return
}

// GetString 从 Context 中获取一个字符串值
// 这是一个线程安全的操作
func (c *Context) GetString(key string) (value string, exists bool) {
	if val, exists := c.Get(key); exists {
		if str, ok := val.(string); ok {
			return str, true
		}
	}
	return "", false
}

// GetInt 从 Context 中获取一个 int 值
// 这是一个线程安全的操作
func (c *Context) GetInt(key string) (value int, exists bool) {
	if val, exists := c.Get(key); exists {
		if i, ok := val.(int); ok {
			return i, true
		}
	}
	return 0, false
}

// GetBool 从 Context 中获取一个 bool 值
// 这是一个线程安全的操作
func (c *Context) GetBool(key string) (value bool, exists bool) {
	if val, exists := c.Get(key); exists {
		if b, ok := val.(bool); ok {
			return b, true
		}
	}
	return false, false
}

// GetFloat64 从 Context 中获取一个 float64 值
// 这是一个线程安全的操作
func (c *Context) GetFloat64(key string) (value float64, exists bool) {
	if val, exists := c.Get(key); exists {
		if f, ok := val.(float64); ok {
			return f, true
		}
	}
	return 0.0, false
}

// GetTime 从 Context 中获取一个 time.Time 值
// 这是一个线程安全的操作
func (c *Context) GetTime(key string) (value time.Time, exists bool) {
	if val, exists := c.Get(key); exists {
		if t, ok := val.(time.Time); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// GetDuration 从 Context 中获取一个 time.Duration 值
// 这是一个线程安全的操作
func (c *Context) GetDuration(key string) (value time.Duration, exists bool) {
	if val, exists := c.Get(key); exists {
		if d, ok := val.(time.Duration); ok {
			return d, true
		}
	}
	return 0, false
}

// MustGet 从 Context 中获取一个值，如果不存在则 panic
// 适用于确定值一定存在的场景
func (c *Context) MustGet(key string) any {
	if value, exists := c.Get(key); exists {
		return value
	}
	panic("Key \"" + key + "\" does not exist in context.")
}

// SetMaxRequestBodySize
func (c *Context) SetMaxRequestBodySize(size int64) {
	c.MaxRequestBodySize = size
}

// Query 从 URL 查询参数中获取值
// 懒加载解析查询参数，并进行缓存
func (c *Context) Query(key string) string {
	if c.queryCache == nil {
		c.queryCache = c.Request.URL.Query() // 首次访问时解析并缓存
	}
	return c.queryCache.Get(key)
}

// DefaultQuery 从 URL 查询参数中获取值，如果不存在则返回默认值
func (c *Context) DefaultQuery(key, defaultValue string) string {
	if value := c.Query(key); value != "" {
		return value
	}
	return defaultValue
}

// PostForm 从 POST 请求体中获取表单值
// 懒加载解析表单数据，并进行缓存
func (c *Context) PostForm(key string) string {
	if c.formCache == nil {
		c.Request.ParseMultipartForm(defaultMemory) // 解析 multipart/form-data 或 application/x-www-form-urlencoded
		c.formCache = c.Request.PostForm
	}
	return c.formCache.Get(key)
}

// DefaultPostForm 从 POST 请求体中获取表单值，如果不存在则返回默认值
func (c *Context) DefaultPostForm(key, defaultValue string) string {
	if value := c.PostForm(key); value != "" {
		return value
	}
	return defaultValue
}

// Param 从 URL 路径参数中获取值
// 例如，对于路由 /users/:id，c.Param("id") 可以获取 id 的值
func (c *Context) Param(key string) string {
	return c.Params.ByName(key)
}

// Raw 向响应写入bytes
func (c *Context) Raw(code int, contentType string, data []byte) {
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.WriteHeader(code)
	c.Writer.Write(data)
}

// String 向响应写入格式化的字符串
func (c *Context) String(code int, format string, values ...any) {
	c.Writer.WriteHeader(code)
	c.Writer.Write([]byte(fmt.Sprintf(format, values...)))
}

// Text 向响应写入无需格式化的string
func (c *Context) Text(code int, text string) {
	c.Writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.Writer.WriteHeader(code)
	c.Writer.Write([]byte(text))
}

// FileText
func (c *Context) FileText(code int, filePath string) {
	// 清理path
	cleanPath := filepath.Clean(filePath)
	if !filepath.IsAbs(cleanPath) {
		c.AddError(fmt.Errorf("relative path not allowed: %s", cleanPath))
		c.ErrorUseHandle(http.StatusBadRequest, fmt.Errorf("relative path not allowed"))
		return
	}
	// 检查文件是否存在
	if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
		c.AddError(fmt.Errorf("file not found: %s", cleanPath))
		c.ErrorUseHandle(http.StatusNotFound, fmt.Errorf("file not found"))
		return
	}

	// 打开文件
	file, err := os.Open(cleanPath)
	if err != nil {
		c.AddError(fmt.Errorf("failed to open file %s: %w", cleanPath, err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to open file: %w", err))
		return
	}
	defer file.Close()

	// 获取文件信息以获取文件大小
	fileInfo, err := file.Stat()
	if err != nil {
		c.AddError(fmt.Errorf("failed to get file info for %s: %w", cleanPath, err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to get file info: %w", err))
		return
	}
	// 判断是否是dir
	if fileInfo.IsDir() {
		c.AddError(fmt.Errorf("path is a directory, not a file: %s", cleanPath))
		c.ErrorUseHandle(http.StatusBadRequest, fmt.Errorf("path is a directory"))
		return
	}

	c.SetHeader("Content-Type", "text/plain; charset=utf-8")

	c.SetBodyStream(file, int(fileInfo.Size()))
}

/*
// not fot work
// FileTextSafeDir
func (c *Context) FileTextSafeDir(code int, filePath string, safeDir string) {

	// 清理path
	cleanPath := path.Clean(filePath)
	if !filepath.IsAbs(cleanPath) {
		c.AddError(fmt.Errorf("relative path not allowed: %s", cleanPath))
		c.ErrorUseHandle(http.StatusBadRequest, fmt.Errorf("relative path not allowed"))
		return
	}
	if strings.Contains(cleanPath, "..") {
		c.AddError(fmt.Errorf("path traversal attempt detected: %s", cleanPath))
		c.ErrorUseHandle(http.StatusBadRequest, fmt.Errorf("path traversal attempt detected"))
		return
	}

	// 判断filePath是否包含在safeDir内, 防止路径穿越
	relPath, err := filepath.Rel(safeDir, cleanPath)
	if err != nil {
		c.AddError(fmt.Errorf("failed to get relative path: %w", err))
		c.ErrorUseHandle(http.StatusBadRequest, fmt.Errorf("failed to get relative path: %w", err))
		return
	}
	cleanPath = filepath.Join(safeDir, relPath)

	// 检查文件是否存在
	if _, err := os.Stat(cleanPath); os.IsNotExist(err) {
		c.AddError(fmt.Errorf("file not found: %s", cleanPath))
		c.ErrorUseHandle(http.StatusNotFound, fmt.Errorf("file not found"))
		return
	}

	// 打开文件
	file, err := os.Open(cleanPath)
	if err != nil {
		c.AddError(fmt.Errorf("failed to open file %s: %w", cleanPath, err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to open file: %w", err))
		return
	}
	defer file.Close()

	// 获取文件信息以获取文件大小
	fileInfo, err := file.Stat()
	if err != nil {
		c.AddError(fmt.Errorf("failed to get file info for %s: %w", cleanPath, err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to get file info: %w", err))
		return
	}
	// 判断是否是dir
	if fileInfo.IsDir() {
		c.AddError(fmt.Errorf("path is a directory, not a file: %s", cleanPath))
		c.ErrorUseHandle(http.StatusBadRequest, fmt.Errorf("path is a directory"))
		return
	}

	c.SetHeader("Content-Type", "text/plain; charset=utf-8")

	c.SetBodyStream(file, int(fileInfo.Size()))
}
*/

// JSON 向响应写入 JSON 数据
// 设置 Content-Type 为 application/json
func (c *Context) JSON(code int, obj any) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(code)
	if err := json.MarshalWrite(c.Writer, obj); err != nil {
		c.AddError(fmt.Errorf("failed to marshal JSON: %w", err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to marshal JSON: %w", err))
		return
	}
}

// GOB 向响应写入GOB数据
// 设置 Content-Type 为 application/octet-stream
func (c *Context) GOB(code int, obj any) {
	c.Writer.Header().Set("Content-Type", "application/octet-stream") // 设置合适的 Content-Type
	c.Writer.WriteHeader(code)
	// GOB 编码
	encoder := gob.NewEncoder(c.Writer)
	if err := encoder.Encode(obj); err != nil {
		c.AddError(fmt.Errorf("failed to encode GOB: %w", err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to encode GOB: %w", err))
		return
	}
}

// WANF向响应写入WANF数据
// 设置 application/vnd.wjqserver.wanf; charset=utf-8
func (c *Context) WANF(code int, obj any) {
	c.Writer.Header().Set("Content-Type", "application/vnd.wjqserver.wanf; charset=utf-8")
	c.Writer.WriteHeader(code)
	// WANF 编码
	encoder := wanf.NewStreamEncoder(c.Writer)
	if err := encoder.Encode(obj); err != nil {
		c.AddError(fmt.Errorf("failed to encode WANF: %w", err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to encode WANF: %w", err))
		return
	}
}

// HTML 渲染 HTML 模板
// 如果 Engine 配置了 HTMLRender，则使用它进行渲染
// 否则，会进行简单的字符串输出
// 预留接口，可以扩展为支持多种模板引擎
func (c *Context) HTML(code int, name string, obj any) {
	c.Writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Writer.WriteHeader(code)

	if c.engine != nil && c.engine.HTMLRender != nil {
		// 假设 HTMLRender 是一个 *template.Template 实例
		if tpl, ok := c.engine.HTMLRender.(*template.Template); ok {
			err := tpl.ExecuteTemplate(c.Writer, name, obj)
			if err != nil {
				c.AddError(fmt.Errorf("failed to render HTML template '%s': %w", name, err))
				c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to render HTML template '%s': %w", name, err))
			}
			return
		}
		// 可以扩展支持其他渲染器接口
	}
	// 默认简单输出，用于未配置 HTMLRender 的情况
	c.Writer.Write([]byte(fmt.Sprintf("<!-- HTML rendered for %s -->\n<pre>%v</pre>", name, obj)))
}

// Redirect 执行 HTTP 重定向
// code 应为 3xx 状态码 (如 http.StatusMovedPermanently, http.StatusFound)
func (c *Context) Redirect(code int, location string) {
	http.Redirect(c.Writer, c.Request, location, code)
	c.Abort()
	if fl, ok := c.Writer.(http.Flusher); ok {
		fl.Flush()
	}
}

// ShouldBindJSON 尝试将请求体绑定到 JSON 对象
func (c *Context) ShouldBindJSON(obj any) error {
	if c.Request.Body == nil {
		return errors.New("request body is empty")
	}
	err := json.UnmarshalRead(c.Request.Body, obj)
	if err != nil {
		return fmt.Errorf("json binding error: %w", err)
	}
	return nil
}

// ShouldBindWANF
func (c *Context) ShouldBindWANF(obj any) error {
	if c.Request.Body == nil {
		return errors.New("request body is empty")
	}
	decoder, err := wanf.NewStreamDecoder(c.Request.Body)
	if err != nil {
		return fmt.Errorf("failed to create WANF decoder: %w", err)
	}

	if err := decoder.Decode(obj); err != nil {
		return fmt.Errorf("WANF binding error: %w", err)
	}
	return nil
}

// Deprecated: This function is a reserved placeholder for future API extensions
// and is not yet implemented. It will either be properly defined or removed in v2.0.0. Do not use.
// ShouldBind 尝试将请求体绑定到各种类型（JSON, Form, XML 等）
// 这是一个复杂的通用绑定接口，通常根据 Content-Type 或其他头部来判断绑定方式
// 预留接口，可根据项目需求进行扩展
func (c *Context) ShouldBind(obj any) error {
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

// AddError 添加一个错误到 Context
// 允许在处理请求过程中收集多个错误
func (c *Context) AddError(err error) {
	c.Errors = append(c.Errors, err)
}

// Errors 返回 Context 中收集的所有错误
func (c *Context) GetErrors() []error {
	return c.Errors
}

// Client 返回 Engine 提供的 HTTPClient
// 方便在请求处理函数中进行出站 HTTP 请求
func (c *Context) Client() *httpc.Client {
	return c.HTTPClient
}

// Context() 返回请求的上下文，用于取消操作
// 这是 Go 标准库的 `context.Context`，用于请求的取消和超时管理
func (c *Context) Context() context.Context {
	return c.ctx
}

// Done returns a channel that is closed when the request context is cancelled or times out.
// 继承自 `context.Context`
func (c *Context) Done() <-chan struct{} {
	return c.ctx.Done()
}

// Err returns the error, if any, that caused the context to be canceled or to
// time out.
// 继承自 `context.Context`
func (c *Context) Err() error {
	return c.ctx.Err()
}

// Value returns the value associated with this context for key, or nil if no
// value is associated with key.
// 可以用于从 Context 中获取与特定键关联的值，包括 Go 原生 Context 的值和 Touka Context 的 Keys
func (c *Context) Value(key any) any {
	if keyAsString, ok := key.(string); ok {
		if val, exists := c.Get(keyAsString); exists {
			return val
		}
	}
	return c.ctx.Value(key) // 尝试从 Go 原生 Context 中获取值
}

// GetWriter 获得一个 io.Writer 接口，可以直接向响应体写入数据
// 这对于需要自定义流式写入或与其他需要 io.Writer 的库集成非常有用
func (c *Context) GetWriter() io.Writer {
	return c.Writer // ResponseWriter 接口嵌入了 http.ResponseWriter，而 http.ResponseWriter 实现了 io.Writer
}

// WriteStream 接受一个 io.Reader 并将其内容流式传输到响应体
// 返回写入的字节数和可能遇到的错误
// 该方法在开始写入之前，会确保设置 HTTP 状态码为 200 OK
func (c *Context) WriteStream(reader io.Reader) (written int64, err error) {
	// 确保在写入数据前设置状态码
	// WriteHeader 会在第一次写入时被 Write 方法隐式调用，但显式调用可以确保状态码的预期
	if !c.Writer.Written() {
		c.Writer.WriteHeader(http.StatusOK) // 默认 200 OK
	}

	written, err = iox.Copy(c.Writer, reader) // 从 reader 读取并写入 ResponseWriter
	if err != nil {
		c.AddError(fmt.Errorf("failed to write stream: %w", err))
	}
	return written, err
}

// GetReqBody 以获取一个 io.ReadCloser 接口，用于读取请求体
// 注意：请求体只能读取一次
func (c *Context) GetReqBody() io.ReadCloser {
	return c.Request.Body
}

// GetReqBodyFull 读取并返回请求体的所有内容
// 注意：请求体只能读取一次
func (c *Context) GetReqBodyFull() ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}

	var limitBytesReader io.ReadCloser

	if c.MaxRequestBodySize > 0 {
		limitBytesReader = NewMaxBytesReader(c.Request.Body, c.MaxRequestBodySize)
		defer func() {
			err := limitBytesReader.Close()
			if err != nil {
				c.AddError(fmt.Errorf("failed to close request body: %w", err))
			}
		}()
	} else {
		limitBytesReader = c.Request.Body
		defer func() {
			err := limitBytesReader.Close()
			if err != nil {
				c.AddError(fmt.Errorf("failed to close request body: %w", err))
			}
		}()
	}

	data, err := iox.ReadAll(limitBytesReader)
	if err != nil {
		c.AddError(fmt.Errorf("failed to read request body: %w", err))
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	return data, nil
}

// 类似 GetReqBodyFull, 返回 *bytes.Buffer
func (c *Context) GetReqBodyBuffer() (*bytes.Buffer, error) {
	if c.Request.Body == nil {
		return nil, nil
	}

	var limitBytesReader io.ReadCloser

	if c.MaxRequestBodySize > 0 {
		limitBytesReader = NewMaxBytesReader(c.Request.Body, c.MaxRequestBodySize)
		defer func() {
			err := limitBytesReader.Close()
			if err != nil {
				c.AddError(fmt.Errorf("failed to close request body: %w", err))
			}
		}()
	} else {
		limitBytesReader = c.Request.Body
		defer func() {
			err := limitBytesReader.Close()
			if err != nil {
				c.AddError(fmt.Errorf("failed to close request body: %w", err))
			}
		}()
	}

	data, err := iox.ReadAll(limitBytesReader)
	if err != nil {
		c.AddError(fmt.Errorf("failed to read request body: %w", err))
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	return bytes.NewBuffer(data), nil
}

// RequestIP 返回客户端的 IP 地址
// 它会根据 Engine 的配置 (ForwardByClientIP) 尝试从 X-Forwarded-For 或 X-Real-IP 等头部获取，
// 否则回退到 Request.RemoteAddr
func (c *Context) RequestIP() string {
	if c.engine.ForwardByClientIP {
		for _, headerName := range c.engine.RemoteIPHeaders {
			ipValue := c.Request.Header.Get(headerName)
			if ipValue == "" {
				continue // 头部为空, 继续检查下一个
			}

			// 使用索引高效遍历逗号分隔的 IP 列表, 避免 strings.Split 的内存分配
			currentPos := 0
			for currentPos < len(ipValue) {
				nextComma := strings.IndexByte(ipValue[currentPos:], ',')

				var ipSegment string
				if nextComma == -1 {
					// 这是列表中的最后一个 IP
					ipSegment = ipValue[currentPos:]
					currentPos = len(ipValue) // 结束循环
				} else {
					// 截取当前 IP 段
					ipSegment = ipValue[currentPos : currentPos+nextComma]
					currentPos += nextComma + 1 // 移动到下一个 IP 段的起始位置
				}

				// 去除空格并检查是否为空 (例如 "ip1,,ip2")
				trimmedIP := strings.TrimSpace(ipSegment)
				if trimmedIP == "" {
					continue
				}

				// 使用 netip.ParseAddr 进行 IP 地址的解析和验证
				addr, err := netip.ParseAddr(trimmedIP)
				if err == nil {
					// 成功解析到合法的 IP, 立即返回
					return addr.String()
				}
			}
		}
	}

	// 回退到 Request.RemoteAddr 的处理
	// 优先使用 netip.ParseAddrPort, 它比 net.SplitHostPort 更高效且分配更少
	addrp, err := netip.ParseAddrPort(c.Request.RemoteAddr)
	if err == nil {
		// 成功从 "ip:port" 格式中解析出 IP
		return addrp.Addr().String()
	}

	// 如果上面的解析失败 (例如 RemoteAddr 只有 IP, 没有端口),
	// 则尝试将整个字符串作为 IP 地址进行解析
	addr, err := netip.ParseAddr(c.Request.RemoteAddr)
	if err == nil {
		return addr.String()
	}

	// 所有方法都失败, 返回空字符串
	return ""
}

// ClientIP 返回客户端的 IP 地址
// 这是一个别名，与 RequestIP 功能相同
func (c *Context) ClientIP() string {
	return c.RequestIP()
}

// ContentType 返回请求的 Content-Type 头部
func (c *Context) ContentType() string {
	return c.GetReqHeader("Content-Type")
}

// UserAgent 返回请求的 User-Agent 头部
func (c *Context) UserAgent() string {
	return c.GetReqHeader("User-Agent")
}

// Status 设置响应状态码
func (c *Context) Status(code int) {
	c.Writer.WriteHeader(code)
}

// File 将指定路径的文件作为响应发送
// 它会设置 Content-Type 和 Content-Disposition 头部
func (c *Context) File(filepath string) {
	http.ServeFile(c.Writer, c.Request, filepath)
	c.Abort() // 发送文件后中止后续处理
}

// SetHeader 设置响应头部
func (c *Context) SetHeader(key, value string) {
	c.Writer.Header().Set(key, value)
}

// AddHeader 添加响应头部
func (c *Context) AddHeader(key, value string) {
	c.Writer.Header().Add(key, value)
}

// Header 作为SetHeader的别名
func (c *Context) Header(key, value string) {
	c.SetHeader(key, value)
}

// DelHeader 删除响应头部
func (c *Context) DelHeader(key string) {
	c.Writer.Header().Del(key)
}

// GetReqHeader 获取请求头部的值
func (c *Context) GetReqHeader(key string) string {
	return c.Request.Header.Get(key)
}

// SetHeaders 接受headers列表
func (c *Context) SetHeaders(headers map[string][]string) {
	for key, values := range headers {
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}
}

// 获取所有resp Headers
func (c *Context) GetAllRespHeader() http.Header {
	return c.Writer.Header()
}

// GetAllReqHeader 获取所有请求头部
func (c *Context) GetAllReqHeader() http.Header {
	return c.Request.Header
}

// 使用定义的errorHandle来处理error并结束当前handle
func (c *Context) ErrorUseHandle(code int, err error) {
	if c.engine != nil && c.engine.errorHandle.handler != nil {
		c.engine.errorHandle.handler(c, code, err)
		c.Abort()
		return
	} else {
		c.String(code, "%s", http.StatusText(code))
		c.Abort()
	}
}

// GetProtocol 获取当前连接版本
func (c *Context) GetProtocol() string {
	return c.Request.Proto
}

// GetHTTPC 获取框架自带传递的httpc
func (c *Context) GetHTTPC() *httpc.Client {
	return c.HTTPClient
}

// GetLogger 获取engine的Logger
func (c *Context) GetLogger() *reco.Logger {
	return c.engine.LogReco
}

// GetReqQueryString
// GetReqQueryString 返回请求的原始查询字符串
func (c *Context) GetReqQueryString() string {
	return c.Request.URL.RawQuery
}

// SetBodyStream 设置响应体为一个 io.Reader，并指定内容长度
// 如果 contentSize 为 -1，则表示内容长度未知，将使用 Transfer-Encoding: chunked
func (c *Context) SetBodyStream(reader io.Reader, contentSize int) {
	// 如果指定了内容长度且大于等于 0，则设置 Content-Length 头部
	if contentSize >= 0 {
		c.Writer.Header().Set("Content-Length", fmt.Sprintf("%d", contentSize))
	} else {
		// 如果内容长度未知，移除 Content-Length 头部，通常会使用 Transfer-Encoding: chunked
		c.Writer.Header().Del("Content-Length")
	}

	// 确保在写入数据前设置状态码
	if !c.Writer.Written() {
		c.Writer.WriteHeader(http.StatusOK) // 默认 200 OK
	}

	// 将 reader 的内容直接复制到 ResponseWriter
	// ResponseWriter 实现了 io.Writer 接口
	_, err := iox.Copy(c.Writer, reader)
	if err != nil {
		c.AddError(fmt.Errorf("failed to write stream: %w", err))
		// 注意：这里可能无法设置错误状态码，因为头部可能已经发送
		// 可以在调用 SetBodyStream 之前检查错误，或者在中间件中处理 Context.Errors
	}
}

// GetRequestURI 返回请求的原始 URI
func (c *Context) GetRequestURI() string {
	return c.Request.RequestURI
}

// GetRequestURIPath 返回请求的原始 URI 的路径部分
func (c *Context) GetRequestURIPath() string {
	return c.Request.URL.Path
}

// === 文件操作 ===

// 将文件内容作为响应body
func (c *Context) SetRespBodyFile(code int, filePath string) {
	// 清理path
	cleanPath := filepath.Clean(filePath)

	// 打开文件
	file, err := os.Open(cleanPath)
	if err != nil {
		c.AddError(fmt.Errorf("failed to open file %s: %w", cleanPath, err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to open file: %w", err))
		return
	}
	defer file.Close()

	// 获取文件信息以获取文件大小和MIME类型
	fileInfo, err := file.Stat()
	if err != nil {
		c.AddError(fmt.Errorf("failed to get file info for %s: %w", cleanPath, err))
		c.ErrorUseHandle(http.StatusInternalServerError, fmt.Errorf("failed to get file info: %w", err))
		return
	}

	// 尝试根据文件扩展名猜测 Content-Type
	contentType := mime.TypeByExtension(filepath.Ext(cleanPath))
	if contentType == "" {
		// 如果无法猜测，则使用默认的二进制流类型
		contentType = "application/octet-stream"
	}

	// 设置响应头
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))
	// 还可以设置 Content-Disposition 来控制浏览器是下载还是直接显示
	// c.Writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, path.Base(cleanPath)))

	// 设置状态码
	c.Writer.WriteHeader(code)

	// 将文件内容写入响应体
	_, err = iox.Copy(c.Writer, file)
	if err != nil {
		c.AddError(fmt.Errorf("failed to write file %s to response: %w", cleanPath, err))
		// 注意：这里可能无法设置错误状态码，因为头部可能已经发送
		// 可以在调用 SetRespBodyFile 之前检查错误，或者在中间件中处理 Context.Errors
	}
	c.Abort() // 文件发送后中止后续处理
}

// == cookie ===

// SetSameSite 设置响应的 SameSite cookie 属性
func (c *Context) SetSameSite(samesite http.SameSite) {
	c.sameSite = samesite
}

// SetCookie 设置一个 HTTP cookie
func (c *Context) SetCookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool) {
	if path == "" {
		path = "/"
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    url.QueryEscape(value),
		MaxAge:   maxAge,
		Path:     path,
		Domain:   domain,
		SameSite: c.sameSite,
		Secure:   secure,
		HttpOnly: httpOnly,
	})
}

func (c *Context) SetCookieData(cookie *http.Cookie) {
	if cookie.Path == "" {
		cookie.Path = "/"
	}
	if cookie.SameSite == http.SameSiteDefaultMode {
		cookie.SameSite = c.sameSite
	}
	http.SetCookie(c.Writer, cookie)
}

// GetCookie 获取指定名称的 cookie 值
func (c *Context) GetCookie(name string) (string, error) {
	cookie, err := c.Request.Cookie(name)
	if err != nil {
		return "", err
	}
	// 对 cookie 值进行 URL 解码
	value, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return "", fmt.Errorf("failed to unescape cookie value: %w", err)
	}
	return value, nil
}

// DeleteCookie 删除指定名称的 cookie
// 通过设置 MaxAge 为 -1 来删除 cookie
func (c *Context) DeleteCookie(name string) {
	c.SetCookie(name, "", -1, "/", "", false, false) // 设置 MaxAge 为 -1 删除 cookie
}

// === 日志记录 ===
func (c *Context) Debugf(format string, args ...any) {
	c.engine.LogReco.Debugf(format, args...)
}

func (c *Context) Infof(format string, args ...any) {
	c.engine.LogReco.Infof(format, args...)
}

func (c *Context) Warnf(format string, args ...any) {
	c.engine.LogReco.Warnf(format, args...)
}

func (c *Context) Errorf(format string, args ...any) {
	c.engine.LogReco.Errorf(format, args...)
}

func (c *Context) Fatalf(format string, args ...any) {
	c.engine.LogReco.Fatalf(format, args...)
}

func (c *Context) Panicf(format string, args ...any) {
	c.engine.LogReco.Panicf(format, args...)
}
