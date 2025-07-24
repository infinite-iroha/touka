// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"fmt"
	"io"
	"sync/atomic"
)

// ErrBodyTooLarge 是当读取的字节数超过 MaxBytesReader 设置的限制时返回的错误.
// 将其定义为可导出的变量, 方便调用方使用 errors.Is 进行判断.
var ErrBodyTooLarge = fmt.Errorf("body too large")

// maxBytesReader 是一个实现了 io.ReadCloser 接口的结构体.
// 它包装了另一个 io.ReadCloser, 并限制了从其中读取的最大字节数.
type maxBytesReader struct {
	// r 是底层的 io.ReadCloser.
	r io.ReadCloser
	// n 是允许读取的最大字节数.
	n int64
	// read 是一个原子计数器, 用于安全地在多个 goroutine 之间跟踪已读取的字节数.
	read atomic.Int64
}

// NewMaxBytesReader 创建并返回一个 io.ReadCloser, 它从 r 读取数据,
// 但在读取的字节数超过 n 后会返回 ErrBodyTooLarge 错误.
//
// 如果 r 为 nil, 会 panic.
// 如果 n 小于 0, 则读取不受限制, 直接返回原始的 r.
func NewMaxBytesReader(r io.ReadCloser, n int64) io.ReadCloser {
	if r == nil {
		panic("NewMaxBytesReader called with a nil reader")
	}
	// 如果限制为负数, 意味着不限制, 直接返回原始的 ReadCloser.
	if n < 0 {
		return r
	}
	return &maxBytesReader{
		r: r,
		n: n,
	}
}

// Read 方法从底层的 ReadCloser 读取数据, 同时检查是否超过了字节限制.
func (mbr *maxBytesReader) Read(p []byte) (int, error) {
	// 在函数开始时只加载一次原子变量, 减少后续的原子操作开销.
	readSoFar := mbr.read.Load()

	// 快速失败路径: 如果在读取之前就已经达到了限制, 立即返回错误.
	if readSoFar >= mbr.n {
		return 0, ErrBodyTooLarge
	}

	// 计算当前还可以读取多少字节.
	remaining := mbr.n - readSoFar

	// 如果请求读取的长度大于剩余可读长度, 我们需要限制本次读取的长度.
	// 这样可以保证即使 p 很大, 我们也只读取到恰好达到 maxBytes 的字节数.
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}

	// 从底层 Reader 读取数据.
	n, err := mbr.r.Read(p)

	// 如果实际读取到了数据, 更新原子计数器.
	if n > 0 {
		readSoFar = mbr.read.Add(int64(n))
	}

	// 如果底层 Read 返回错误 (例如 io.EOF).
	if err != nil {
		// 如果是 EOF, 并且我们还没有读满 n 个字节, 这是一个正常的结束.
		// 如果已经读满了 n 个字节, 即使是 EOF, 也可以认为成功了.
		return n, err
	}

	// 读后检查: 如果这次读取使得总字节数超过了限制, 返回超限错误.
	// 这是处理"跨越"限制情况的关键.
	if readSoFar > mbr.n {
		// 返回实际读取的字节数 n, 并附上超限错误.
		// 上层调用者知道已经有 n 字节被读入了缓冲区 p, 但流已因超限而关闭.
		return n, ErrBodyTooLarge
	}

	// 一切正常, 返回读取的字节数和 nil 错误.
	return n, nil
}

// Close 方法关闭底层的 ReadCloser, 保证资源释放.
func (mbr *maxBytesReader) Close() error {
	return mbr.r.Close()
}
