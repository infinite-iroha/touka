package touka

import (
	"net/http"
	"sync"
)

// errorCapturingResponseWriter 用于在 FileServer 处理时捕获错误状态码
// 并在用户设置了自定义 ErrorHandler 时, 用该 ErrorHandler 处理此错误
type errorCapturingResponseWriter struct {
	w                   http.ResponseWriter // 原始的 ResponseWriter (通常是 touka.ResponseWriter 实例)
	r                   *http.Request       // 当前请求
	ctx                 *Context            // 当前 touka.Context
	errorHandlerFunc    ErrorHandler        // 实际要调用的错误处理函数
	statusCode          int                 // FileServer 尝试设置的状态码
	headerSnapshot      http.Header         // FileServer 在调用 WriteHeader 前可能设置的头部快照
	capturedErrorSignal bool                // 标记 FileServer 是否意图发送一个错误状态码 (>=400)
	responseStarted     bool                // 标记包装器是否已经向原始 w 发送过任何数据
}

// errorResponseWriterPool 是用于复用 errorCapturingResponseWriter 实例的对象池
var errorResponseWriterPool = sync.Pool{
	New: func() interface{} {
		return &errorCapturingResponseWriter{
			headerSnapshot: make(http.Header), // 预先初始化 map, 减少 reset 时的分配
		}
	},
}

// reset 重置 errorCapturingResponseWriter 的状态以供复用
func (ecw *errorCapturingResponseWriter) reset(w http.ResponseWriter, r *http.Request, ctx *Context, eh ErrorHandler) {
	ecw.w = w
	ecw.r = r
	ecw.ctx = ctx
	ecw.errorHandlerFunc = eh
	ecw.statusCode = 0
	// 清空 headerSnapshot, 但保留底层容量, 避免再次分配
	for k := range ecw.headerSnapshot {
		delete(ecw.headerSnapshot, k)
	}
	ecw.capturedErrorSignal = false
	ecw.responseStarted = false
}

// AcquireErrorCapturingResponseWriter 从对象池获取一个 errorCapturingResponseWriter 实例
// 必须在处理完成后调用 ReleaseErrorCapturingResponseWriter
func AcquireErrorCapturingResponseWriter(c *Context, eh ErrorHandler) *errorCapturingResponseWriter {
	ecw := errorResponseWriterPool.Get().(*errorCapturingResponseWriter)
	ecw.reset(c.Writer, c.Request, c, eh) // 传入 Touka Context 的 Writer
	return ecw
}

// ReleaseErrorCapturingResponseWriter 将一个 errorCapturingResponseWriter 实例返回到对象池
func ReleaseErrorCapturingResponseWriter(ecw *errorCapturingResponseWriter) {
	ecw.reset(nil, nil, nil, nil) // 清空敏感信息
	errorResponseWriterPool.Put(ecw)
}

// Header 返回一个 http.Header
// 如果捕获到错误信号, 则操作内部的快照头部, 因为这些头部可能不会被发送, 或者会被 ErrorHandler 覆盖
// 否则, 代理到原始 ResponseWriter 的 Header()
func (ecw *errorCapturingResponseWriter) Header() http.Header {
	if ecw.capturedErrorSignal {
		return ecw.headerSnapshot
	}
	// 返回原始 ResponseWriter 的 Header(), 确保 FileServer 设置的头部直接作用于最终响应
	return ecw.w.Header()
}

// WriteHeader 记录状态码
// 如果状态码表示错误 (>=400), 则激活 capturedErrorSignal 并不将状态码传递给原始 ResponseWriter
// 如果状态码表示成功, 则将快照中的头部（如果有）复制到原始 w, 然后调用原始 w.WriteHeader
func (ecw *errorCapturingResponseWriter) WriteHeader(statusCode int) {
	if ecw.responseStarted {
		return // 响应已开始, 忽略后续的 WriteHeader 调用
	}
	ecw.statusCode = statusCode // 总是记录 FileServer 意图的状态码

	if statusCode >= http.StatusBadRequest {
		ecw.capturedErrorSignal = true
		// 是一个错误状态码 (>=400), 激活错误信号
		// 不会将这个 WriteHeader 传递给原始的 w, 等待 processAfterFileServer 处理
	} else {
		// 是成功状态码
		// 将 ecw.headerSnapshot 中（由 FileServer 在此之前通过 ecw.Header() 设置的）
		// 任何头部直接复制到原始的 w.Header(), 确保多值头部正确传递
		for k, v := range ecw.headerSnapshot {
			ecw.w.Header()[k] = v // 直接赋值 []string, 保留所有值
		}
		ecw.w.WriteHeader(statusCode) // 实际写入状态码到原始 ResponseWriter
		ecw.responseStarted = true    // 标记成功响应已开始
	}
}

// Write 将数据写入响应
// 如果 capturedErrorSignal 为 true, 则丢弃数据, 因为 ErrorHandlerFunc 将负责响应体
// 如果是成功路径, 则在必要时先发送隐式的 200 OK 头部, 然后将数据写入原始 ResponseWriter
func (ecw *errorCapturingResponseWriter) Write(data []byte) (int, error) {
	if ecw.capturedErrorSignal {
		return len(data), nil // 假装写入成功, 避免 FileServer 内部的错误
	}

	if !ecw.responseStarted {
		if ecw.statusCode == 0 { // 如果 statusCode 仍为0 (WriteHeader 从未被显式调用)
			ecw.statusCode = http.StatusOK // 隐式 200 OK
		}
		// 将 headerSnapshot 中的头部复制到原始 ResponseWriter 的 Header
		for k, v := range ecw.headerSnapshot {
			ecw.w.Header()[k] = v // 直接赋值 []string, 保留所有值
		}
		ecw.w.WriteHeader(ecw.statusCode) // 发送实际的状态码 (可能是 200 或之前设置的 2xx)
		ecw.responseStarted = true
	}
	return ecw.w.Write(data) // 写入数据到原始 ResponseWriter
}

// Flush 尝试刷新缓冲的数据到客户端
// 仅当未捕获错误且响应已开始, 并且原始 ResponseWriter 支持 http.Flusher 时才执行
func (ecw *errorCapturingResponseWriter) Flush() {
	if flusher, ok := ecw.w.(http.Flusher); ok {
		if !ecw.capturedErrorSignal && ecw.responseStarted {
			flusher.Flush()
		}
	}
}

// processAfterFileServer 在 http.FileServer.ServeHTTP 调用完成后执行
// 如果之前捕获了错误信号 (capturedErrorSignal is true) 并且响应尚未开始
// 它将调用配置的 ErrorHandlerFunc 来处理错误
func (ecw *errorCapturingResponseWriter) processAfterFileServer() {
	if ecw.capturedErrorSignal && !ecw.responseStarted {
		if ecw.ctx.engine.noRoute != nil {
			ecw.ctx.Next()
		} else {
			// 调用用户自定义的 ErrorHandlerFunc, 由它负责完整的错误响应
			ecw.errorHandlerFunc(ecw.ctx, ecw.statusCode)
			ecw.ctx.Abort()
		}
	}
	// 如果 !ecw.capturedErrorSignal, 则成功路径已通过代理写入 ecw.w, 无需额外操作
	// 如果 ecw.capturedErrorSignal && ecw.responseStarted, 表示在捕获错误信号之前,
	// 成功路径的响应已经开始, 此时无法再进行错误处理覆盖
}
