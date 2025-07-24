// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"runtime/debug"
)

// --- ResponseWriter 包装 ---

// ResponseWriter 接口扩展了 http.ResponseWriter 以提供对响应状态和大小的访问
type ResponseWriter interface {
	http.ResponseWriter
	http.Hijacker // 支持 WebSocket 等
	http.Flusher  // 支持流式响应

	Status() int   // 返回写入的 HTTP 状态码，如果未写入则为 0
	Size() int     // 返回已写入响应体的字节数
	Written() bool // 返回 WriteHeader 是否已被调用
	IsHijacked() bool
}

// responseWriterImpl 是 ResponseWriter 的具体实现
type responseWriterImpl struct {
	http.ResponseWriter
	size     int
	status   int // 0 表示尚未写入状态码
	hijacked bool
}

// NewResponseWriter 创建并返回一个 responseWriterImpl 实例
func newResponseWriter(w http.ResponseWriter) ResponseWriter {
	return &responseWriterImpl{
		ResponseWriter: w,
		status:         0, // 明确初始状态
		size:           0,
		hijacked:       false,
	}
}

func (rw *responseWriterImpl) reset(w http.ResponseWriter) {
	rw.ResponseWriter = w
	rw.status = 0
	rw.size = 0
	rw.hijacked = false
}

func (rw *responseWriterImpl) WriteHeader(statusCode int) {
	if rw.hijacked {
		return
	}
	if rw.status == 0 { // 确保只设置一次
		rw.status = statusCode
		rw.ResponseWriter.WriteHeader(statusCode)
	}
}

func (rw *responseWriterImpl) Write(b []byte) (int, error) {
	if rw.hijacked {
		return 0, errors.New("http: response already hijacked")
	}
	if rw.status == 0 {
		// 如果 WriteHeader 没被显式调用，Go 的 http server 会默认为 200
		// 我们在这里也将其标记为 200，因为即将写入数据
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

// Hijack 实现 http.Hijacker 接口
func (rw *responseWriterImpl) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	// 检查是否已劫持
	if rw.hijacked {
		return nil, nil, errors.New("http: connection already hijacked")
	}

	// 尝试从底层 ResponseWriter 获取 Hijacker 接口
	hj, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("http.Hijacker interface not supported")
	}

	// 调用底层的 Hijack 方法
	conn, brw, err := hj.Hijack()
	if err != nil {
		// 如果劫持失败，返回错误
		return nil, nil, err
	}

	// 如果劫持成功，更新内部状态
	rw.hijacked = true

	return conn, brw, nil
}

// Flush 实现 http.Flusher 接口
func (rw *responseWriterImpl) Flush() {
	defer func() {
		if r := recover(); r != nil {
			// 记录捕获到的 panic 信息，这表明底层连接可能已经关闭或失效
			// 使用 log.Printf 记录，并包含堆栈信息，便于调试
			log.Printf("Recovered from panic during responseWriterImpl.Flush for request: %v\nStack: %s", r, debug.Stack())
			// 捕获后，不继续传播 panic，允许请求的 goroutine 优雅退出
		}
	}()
	if rw.hijacked {
		return
	}
	if fl, ok := rw.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// IsHijacked 方法返回连接是否已被劫持
func (rw *responseWriterImpl) IsHijacked() bool {
	return rw.hijacked
}
