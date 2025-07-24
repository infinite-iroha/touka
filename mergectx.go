// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"context"
	"sync"
	"time"
)

// mergedContext 实现了 context.Context 接口, 是 Merge 函数返回的实际类型.
type mergedContext struct {
	// 嵌入一个基础 context, 它持有最早的 deadline 和取消信号.
	context.Context
	// 保存了所有的父 context, 用于 Value() 方法的查找.
	parents []context.Context
	// 用于手动取消此 mergedContext 的函数.
	cancel context.CancelFunc
}

// MergeCtx 创建并返回一个新的 context.Context.
// 这个新的 context 会在任何一个传入的父 contexts 被取消时, 或者当返回的 CancelFunc 被调用时,
// 自动被取消 (逻辑或关系).
//
// 新的 context 会继承:
// - Deadline: 所有父 context 中最早的截止时间.
// - Value: 按传入顺序从第一个能找到值的父 context 中获取值.
func MergeCtx(parents ...context.Context) (ctx context.Context, cancel context.CancelFunc) {
	if len(parents) == 0 {
		return context.WithCancel(context.Background())
	}
	if len(parents) == 1 {
		return context.WithCancel(parents[0])
	}

	var earliestDeadline time.Time
	for _, p := range parents {
		if deadline, ok := p.Deadline(); ok {
			if earliestDeadline.IsZero() || deadline.Before(earliestDeadline) {
				earliestDeadline = deadline
			}
		}
	}

	var baseCtx context.Context
	var baseCancel context.CancelFunc
	if !earliestDeadline.IsZero() {
		baseCtx, baseCancel = context.WithDeadline(context.Background(), earliestDeadline)
	} else {
		baseCtx, baseCancel = context.WithCancel(context.Background())
	}

	mc := &mergedContext{
		Context: baseCtx,
		parents: parents,
		cancel:  baseCancel,
	}

	// 启动一个监控 goroutine.
	go func() {
		defer mc.cancel()

		// orDone 会返回一个 channel, 当任何一个父 context 被取消时, 这个 channel 就会关闭.
		// 同时监听 baseCtx.Done() 以便支持手动取消.
		select {
		case <-orDone(mc.parents...):
		case <-mc.Context.Done():
		}
	}()

	return mc, mc.cancel
}

// Value 返回当前Ctx Value
func (mc *mergedContext) Value(key any) any {
	return mc.Context.Value(key)
}

// Deadline 实现了 context.Context 的 Deadline 方法.
func (mc *mergedContext) Deadline() (deadline time.Time, ok bool) {
	return mc.Context.Deadline()
}

// Done 实现了 context.Context 的 Done 方法.
func (mc *mergedContext) Done() <-chan struct{} {
	return mc.Context.Done()
}

// Err 实现了 context.Context 的 Err 方法.
func (mc *mergedContext) Err() error {
	return mc.Context.Err()
}

// orDone 是一个辅助函数, 返回一个 channel.
// 当任意一个输入 context 的 Done() channel 关闭时, orDone 返回的 channel 也会关闭.
// 这是一个非阻塞的、不会泄漏 goroutine 的实现.
func orDone(contexts ...context.Context) <-chan struct{} {
	done := make(chan struct{})

	var once sync.Once
	closeDone := func() {
		once.Do(func() {
			close(done)
		})
	}

	// 为每个父 context 启动一个 goroutine.
	for _, ctx := range contexts {
		go func(c context.Context) {
			select {
			case <-c.Done():
				closeDone()
			case <-done:
				// orDone 已经被其他 goroutine 关闭了, 当前 goroutine 可以安全退出.
			}
		}(ctx)
	}

	return done
}
