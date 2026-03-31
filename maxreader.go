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
	// emptyAtLimit 记录在达到上限后是否已经遇到过一次 0,nil 读.
	emptyAtLimit atomic.Bool
}

// NewMaxBytesReader 创建并返回一个 io.ReadCloser, 它从 r 读取数据,
// 但在读取的字节数超过 n 后会返回 ErrBodyTooLarge 错误.
//
// 如果 r 为 nil, 会 panic.
// 如果 n 小于等于 0, 则读取不受限制, 直接返回原始的 r.
func NewMaxBytesReader(r io.ReadCloser, n int64) io.ReadCloser {
	if r == nil {
		panic("NewMaxBytesReader called with a nil reader")
	}
	// 如果限制为非正数, 意味着不限制, 直接返回原始的 ReadCloser.
	if n <= 0 {
		return r
	}
	return &maxBytesReader{
		r: r,
		n: n,
	}
}

// Read 方法从底层的 ReadCloser 读取数据, 同时检查是否超过了字节限制.
func (mbr *maxBytesReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	// 在函数开始时只加载一次原子变量, 减少后续的原子操作开销.
	readSoFar := mbr.read.Load()
	remaining := mbr.n - readSoFar
	if remaining < 0 {
		return 0, ErrBodyTooLarge
	}
	if remaining == 0 {
		var probe [1]byte
		n, err := mbr.r.Read(probe[:])
		if n > 0 {
			mbr.read.Add(int64(n))
			return 0, ErrBodyTooLarge
		}
		if err != nil {
			return 0, err
		}
		if mbr.emptyAtLimit.Swap(true) {
			return 0, ErrBodyTooLarge
		}
		return 0, nil
	}
	mbr.emptyAtLimit.Store(false)

	// 最多多读一个字节, 以区分“恰好到上限”和“已经超限”。
	if int64(len(p))-1 > remaining {
		p = p[:remaining+1]
	}

	// 从底层 Reader 读取数据.
	n, err := mbr.r.Read(p)

	if int64(n) <= remaining {
		if n > 0 {
			mbr.read.Add(int64(n))
		}
		return n, err
	}

	// 读取结果跨过了限制，只向上层暴露允许的部分。
	if remaining > 0 {
		mbr.read.Add(remaining)
	}
	return int(remaining), ErrBodyTooLarge
}

// Close 方法关闭底层的 ReadCloser, 保证资源释放.
func (mbr *maxBytesReader) Close() error {
	return mbr.r.Close()
}
