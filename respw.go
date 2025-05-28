package touka

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// --- ResponseWriter 包装 ---

// ResponseWriter 接口扩展了 http.ResponseWriter 以提供对响应状态和大小的访问。
type ResponseWriter interface {
	http.ResponseWriter
	http.Hijacker // 支持 WebSocket 等
	http.Flusher  // 支持流式响应

	Status() int   // 返回写入的 HTTP 状态码，如果未写入则为 0
	Size() int     // 返回已写入响应体的字节数
	Written() bool // 返回 WriteHeader 是否已被调用
}

// responseWriterImpl 是 ResponseWriter 的具体实现。
type responseWriterImpl struct {
	http.ResponseWriter
	size   int
	status int // 0 表示尚未写入状态码
}

// NewResponseWriter 创建并返回一个 responseWriterImpl 实例。
func newResponseWriter(w http.ResponseWriter) ResponseWriter {
	rw := &responseWriterImpl{
		ResponseWriter: w,
		status:         0, // 明确初始状态
		size:           0,
	}
	return rw
}

func (rw *responseWriterImpl) WriteHeader(statusCode int) {
	if rw.status == 0 { // 确保只设置一次
		rw.status = statusCode
		rw.ResponseWriter.WriteHeader(statusCode)
	}
}

func (rw *responseWriterImpl) Write(b []byte) (int, error) {
	if rw.status == 0 {
		// 如果 WriteHeader 没被显式调用，Go 的 http server 会默认为 200
		// 我们在这里也将其标记为 200，因为即将写入数据。
		rw.status = http.StatusOK
		// ResponseWriter.Write 会在第一次写入时自动调用 WriteHeader(http.StatusOK)
		// 所以不需要在这里显式调用 rw.ResponseWriter.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.size += n
	return n, err
}

func (rw *responseWriterImpl) Status() int {
	return rw.status
}

func (rw *responseWriterImpl) Size() int {
	return rw.size
}

func (rw *responseWriterImpl) Written() bool {
	return rw.status != 0
}

// Hijack 实现 http.Hijacker 接口。
func (rw *responseWriterImpl) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("http.Hijacker interface not supported")
}

// Flush 实现 http.Flusher 接口。
func (rw *responseWriterImpl) Flush() {
	if fl, ok := rw.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}
