package touka

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const (
	headerAcceptEncoding  = "Accept-Encoding"  // 请求头部，客户端声明接受的编码
	headerContentEncoding = "Content-Encoding" // 响应头部，服务器声明使用的编码
	headerContentLength   = "Content-Length"   // 响应头部，内容长度
	headerContentType     = "Content-Type"     // 响应头部，内容类型
	headerVary            = "Vary"             // 响应头部，指示缓存行为
	encodingGzip          = "gzip"             // Gzip 编码名称
)

var (
	// 默认可压缩的 MIME 类型
	defaultCompressibleTypes = []string{
		"text/html", "text/css", "text/plain", "text/javascript",
		"application/javascript", "application/x-javascript", "application/json",
		"application/xml", "image/svg+xml",
	}
)

// GzipOptions 用于配置 Gzip 中间件。
type GzipOptions struct {
	// Level 设置 Gzip 压缩级别。
	// 例如: gzip.DefaultCompression, gzip.BestSpeed, gzip.BestCompression。
	Level int
	// MinContentLength 是应用 Gzip 的最小内容长度。
	// 如果响应的 Content-Length 小于此值，则不应用 Gzip。
	// 默认为 0 (无最小长度限制)。
	MinContentLength int64
	// CompressibleTypes 是要压缩的 MIME 类型列表。
	// 如果为空，将使用 defaultCompressibleTypes。
	CompressibleTypes []string
	// DecompressFn 是一个可选函数，用于解压缩请求体 (如果请求体是 gzipped)。
	// 如果为 nil，则禁用请求体解压缩。
	// 注意: 本次实现主要关注响应压缩，请求解压可以作为扩展。
	// DecompressFn func(c *Context)
}

// gzipResponseWriter 包装了 touka.ResponseWriter 以提供 Gzip 压缩功能。
type gzipResponseWriter struct {
	ResponseWriter              // 底层的 ResponseWriter (可能是 ecw 或 responseWriterImpl)
	gzWriter       *gzip.Writer // compress/gzip 的 writer
	options        *GzipOptions // Gzip 配置
	wroteHeader    bool         // 标记 Header 是否已写入
	doCompression  bool         // 标记是否执行压缩
	statusCode     int          // 存储状态码，在实际写入底层 Writer 前使用
}

// --- 对象池 ---
var gzipResponseWriterPool = sync.Pool{
	New: func() interface{} {
		return &gzipResponseWriter{}
	},
}

// gzip.Writer 实例的对象池。
// 注意: gzip.Writer.Reset() 不会改变压缩级别，所以对象池需要提供已正确初始化级别的 writer。
// 我们为每个可能的级别创建一个池。
var gzipWriterPools [gzip.BestCompression - gzip.BestSpeed + 2]*sync.Pool // 覆盖 -1 (Default) 到 9 (BestCompression)

func init() {
	for i := gzip.BestSpeed; i <= gzip.BestCompression; i++ {
		level := i // 捕获循环变量用于闭包
		gzipWriterPools[level-gzip.BestSpeed] = &sync.Pool{
			New: func() interface{} {
				// 初始化时 writer 为 nil，在 Reset 时设置
				w, _ := gzip.NewWriterLevel(nil, level)
				return w
			},
		}
	}
	// 为 gzip.DefaultCompression (-1) 映射一个索引
	defaultLevelIndex := gzip.BestCompression - gzip.BestSpeed + 1
	gzipWriterPools[defaultLevelIndex] = &sync.Pool{
		New: func() interface{} {
			w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
			return w
		},
	}
}

// 从对象池获取一个 gzip.Writer
func getGzipWriterFromPool(level int, underlyingWriter io.Writer) *gzip.Writer {
	var poolIndex int
	if level == gzip.DefaultCompression {
		poolIndex = gzip.BestCompression - gzip.BestSpeed + 1
	} else if level >= gzip.BestSpeed && level <= gzip.BestCompression {
		poolIndex = level - gzip.BestSpeed
	} else { // 无效级别，使用默认级别
		poolIndex = gzip.BestCompression - gzip.BestSpeed + 1
		level = gzip.DefaultCompression // 保证一致性
	}

	gz := gzipWriterPools[poolIndex].Get().(*gzip.Writer)
	gz.Reset(underlyingWriter) // 重置并关联到底层的 io.Writer
	return gz
}

// 将 gzip.Writer 返还给对象池
func putGzipWriterToPool(gz *gzip.Writer, level int) {
	var poolIndex int
	if level == gzip.DefaultCompression {
		poolIndex = gzip.BestCompression - gzip.BestSpeed + 1
	} else if level >= gzip.BestSpeed && level <= gzip.BestCompression {
		poolIndex = level - gzip.BestSpeed
	} else { // 不应该发生，如果 getGzipWriterFromPool 进行了标准化
		poolIndex = gzip.BestCompression - gzip.BestSpeed + 1
	}
	gzipWriterPools[poolIndex].Put(gz)
}

// 从对象池获取一个 gzipResponseWriter
func acquireGzipResponseWriter(underlying ResponseWriter, opts *GzipOptions) *gzipResponseWriter {
	gzw := gzipResponseWriterPool.Get().(*gzipResponseWriter)
	gzw.ResponseWriter = underlying
	gzw.options = opts
	gzw.wroteHeader = false
	gzw.doCompression = false
	gzw.statusCode = 0 // 重置状态码
	// gzWriter 将在 WriteHeader 中如果需要时获取
	return gzw
}

// 将 gzipResponseWriter 返还给对象池
func releaseGzipResponseWriter(gzw *gzipResponseWriter) {
	if gzw.gzWriter != nil {
		// 确保它被关闭并返回到池中
		_ = gzw.gzWriter.Close() // 关闭会 flush
		putGzipWriterToPool(gzw.gzWriter, gzw.options.Level)
		gzw.gzWriter = nil
	}
	gzw.ResponseWriter = nil // 断开引用
	gzw.options = nil
	gzipResponseWriterPool.Put(gzw)
}

// --- gzipResponseWriter 方法实现 ---

// Header 返回底层 ResponseWriter 的头部 map。
func (gzw *gzipResponseWriter) Header() http.Header {
	return gzw.ResponseWriter.Header()
}

// WriteHeader 发送 HTTP 响应头部和指定的状态码。
// 在这里决定是否进行压缩。
func (gzw *gzipResponseWriter) WriteHeader(statusCode int) {
	if gzw.wroteHeader {
		return
	}
	gzw.wroteHeader = true
	gzw.statusCode = statusCode // 存储状态码

	// 在修改头部以进行压缩之前进行条件检查
	// 1. 如果状态码是信息性(1xx)、重定向(3xx)、无内容(204)、重置内容(205)或未修改(304)，则不压缩
	if statusCode < http.StatusOK || statusCode == http.StatusNoContent || statusCode == http.StatusResetContent || statusCode == http.StatusNotModified {
		gzw.ResponseWriter.WriteHeader(statusCode)
		return
	}
	// 2. 如果响应已经被编码，则不压缩
	if gzw.Header().Get(headerContentEncoding) != "" {
		gzw.ResponseWriter.WriteHeader(statusCode)
		return
	}
	// 3. 检查 Content-Type
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(gzw.Header().Get(headerContentType), ";")[0]))
	compressibleTypes := gzw.options.CompressibleTypes
	if len(compressibleTypes) == 0 {
		compressibleTypes = defaultCompressibleTypes
	}
	isCompressible := false
	for _, t := range compressibleTypes {
		if strings.HasPrefix(contentType, t) { // 使用 HasPrefix 以匹配如 "text/html; charset=utf-8"
			isCompressible = true
			break
		}
	}
	if !isCompressible {
		gzw.ResponseWriter.WriteHeader(statusCode)
		return
	}
	// 4. 检查 MinContentLength
	if gzw.options.MinContentLength > 0 {
		if clStr := gzw.Header().Get(headerContentLength); clStr != "" {
			if cl, err := strconv.ParseInt(clStr, 10, 64); err == nil && cl < gzw.options.MinContentLength {
				gzw.ResponseWriter.WriteHeader(statusCode)
				return
			}
		}
		// 如果未设置 Content-Length，但设置了 MinContentLength，我们可能仍会压缩。
		// 这是一个权衡：可能会压缩小的动态内容。
	}

	// 所有检查通过，进行压缩
	gzw.doCompression = true
	gzw.Header().Set(headerContentEncoding, encodingGzip)
	gzw.Header().Add(headerVary, headerAcceptEncoding) // 使用 Add 以避免覆盖其他 Vary 值
	gzw.Header().Del(headerContentLength)              // Gzip 会改变内容长度，所以删除它

	// 从池中获取 gzWriter，并将其 Reset 指向实际的底层 ResponseWriter
	// 注意：gzw.ResponseWriter 是被 Gzip 包装的 writer (例如，原始的 responseWriterImpl 或 ecw)
	gzw.gzWriter = getGzipWriterFromPool(gzw.options.Level, gzw.ResponseWriter)
	gzw.ResponseWriter.WriteHeader(statusCode) // 调用原始的 WriteHeader
}

// Write 将数据写入连接作为 HTTP 回复的一部分。
func (gzw *gzipResponseWriter) Write(data []byte) (int, error) {
	if !gzw.wroteHeader {
		// 如果在 WriteHeader 之前调用 Write，根据 http.ResponseWriter 规范，
		// 应写入 200 OK 头部。
		gzw.WriteHeader(http.StatusOK)
	}

	if gzw.doCompression {
		return gzw.gzWriter.Write(data)
	}
	return gzw.ResponseWriter.Write(data)
}

// Close 确保 gzip writer 被关闭并释放资源。
// 中间件应该在 c.Next() 之后调用它（通常在 defer 中）。
func (gzw *gzipResponseWriter) Close() error {
	if gzw.gzWriter != nil {
		err := gzw.gzWriter.Close() // Close 会 Flush
		putGzipWriterToPool(gzw.gzWriter, gzw.options.Level)
		gzw.gzWriter = nil // 标记为已返回
		return err
	}
	return nil
}

// Flush 将所有缓冲数据发送到客户端。
// 实现 http.Flusher。
func (gzw *gzipResponseWriter) Flush() {
	if gzw.doCompression && gzw.gzWriter != nil {
		_ = gzw.gzWriter.Flush() // 确保 gzip writer 的缓冲被刷新
	}
	// 然后刷新底层的 writer (如果它支持)
	if fl, ok := gzw.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// Hijack 允许调用者接管连接。
// 实现 http.Hijacker。
func (gzw *gzipResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	// 如果正在压缩，hijack 的意义不大或不安全。
	// 然而，WriteHeader 应该会阻止对 101 状态码的压缩。
	// 此调用必须转到实际的底层 ResponseWriter。
	if hj, ok := gzw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	// 返回英文错误
	return nil, nil, errors.New("touka.gzipResponseWriter: underlying ResponseWriter does not implement http.Hijacker")
}

// Status 返回已写入的 HTTP 状态码。委托给底层的 ResponseWriter。
// 这确保了与 ecw 或其他可能跟踪状态的包装器的兼容性。
func (gzw *gzipResponseWriter) Status() int {
	if gzw.statusCode != 0 { // 如果我们在 WriteHeader 期间存储了它
		return gzw.statusCode
	}
	return gzw.ResponseWriter.Status() // 委托
}

// Size 返回已写入的字节数。委托给底层的 ResponseWriter。
// 如果已压缩，这将是压缩后的大小 (由底层 writer 记录)。
func (gzw *gzipResponseWriter) Size() int {
	return gzw.ResponseWriter.Size() // GzipResponseWriter 本身不直接跟踪大小，依赖底层
}

// Written 返回 WriteHeader 是否已被调用。委托给底层的 ResponseWriter。
func (gzw *gzipResponseWriter) Written() bool {
	// 如果 gzw.wroteHeader 为 true，说明 WriteHeader 至少被 gzw 处理过。
	// 但最终是否写入底层取决于 gzw 的逻辑。
	// 更可靠的是询问底层 writer。
	return gzw.ResponseWriter.Written() // 委托
}

// --- Gzip 中间件 ---

// Gzip 返回一个使用 Gzip 压缩 HTTP 响应的中间件。
// 它会检查客户端的 "Accept-Encoding" 头部和响应的 "Content-Type"
// 来决定是否应用压缩。
// level 参数指定压缩级别 (例如 gzip.DefaultCompression)。
// opts 参数是可选的 GzipOptions。
func Gzip(level int, opts ...GzipOptions) HandlerFunc {
	config := GzipOptions{ // 初始化默认配置
		Level:             level,
		MinContentLength:  0, // 默认：无最小长度
		CompressibleTypes: defaultCompressibleTypes,
	}
	if len(opts) > 0 { // 如果传入了 GzipOptions，则覆盖默认值
		opt := opts[0]
		config.Level = opt.Level // 允许通过结构体覆盖级别
		if opt.MinContentLength > 0 {
			config.MinContentLength = opt.MinContentLength
		}
		if len(opt.CompressibleTypes) > 0 {
			config.CompressibleTypes = opt.CompressibleTypes
		}
	}
	// 验证级别
	if config.Level < gzip.DefaultCompression || config.Level > gzip.BestCompression {
		config.Level = gzip.DefaultCompression
	}

	return func(c *Context) {
		// 1. 检查客户端是否接受 gzip
		if !strings.Contains(c.Request.Header.Get(headerAcceptEncoding), encodingGzip) {
			c.Next()
			return
		}

		// 2. 包装 ResponseWriter
		originalWriter := c.Writer
		gzw := acquireGzipResponseWriter(originalWriter, &config)
		c.Writer = gzw // 替换上下文的 writer

		// defer 确保即使后续处理函数发生 panic，也能进行清理，
		// 尽管恢复中间件应该自己处理 panic 响应。
		defer func() {
			// 必须关闭 gzip writer 以刷新其缓冲区。
			// 这也会将 gzip.Writer 返回到其对象池。
			if err := gzw.Close(); err != nil {
				// 记录关闭 gzip writer 时的错误，但不应覆盖已发送的响应
				// 通常这个错误不严重，因为数据可能已经大部分发送
				// 使用英文记录日志
				// log.Printf("Error closing gzip writer: %v", err)
				c.AddError(err) // 可以选择将错误添加到 Context 中
			}

			// 恢复原始 writer 并将 gzipResponseWriter 返回到其对象池
			c.Writer = originalWriter
			releaseGzipResponseWriter(gzw)
		}()

		// 3. 调用链中的下一个处理函数
		c.Next()

		// c.Next() 执行完毕后，响应头部应该已经设置。
		// gzw.WriteHeader 会被显式调用或通过第一次 Write 隐式调用。
		// 如果 gzw.doCompression 为 true，响应体已写入 gzw.gzWriter。
		// defer 中的 gzw.Close() 会刷新最终的压缩字节。
	}
}
