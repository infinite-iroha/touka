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
	// 嵌入一个基础 context, 用于 Deadline() 和 Value() 查找.
	context.Context
	// 保存了所有的父 context, 用于 Value() 方法的查找.
	parents []context.Context
	// cancelCtx 由 CancelCause 管理, 当 cause 取消时其 Done() 关闭.
	cancelCtx context.Context
	// deadlineCtx 仅在有 deadline 时非 nil, 用于检测 deadline 到期.
	deadlineCtx context.Context
	// done 缓存 Done() 的 channel, 避免重复创建 orDone goroutine.
	done     <-chan struct{}
	doneOnce sync.Once
}

// MergeCtx 创建并返回一个新的 context.Context.
// 这个新的 context 会在任何一个传入的父 contexts 被取消时, 或者当返回的 CancelFunc 被调用时,
// 自动被取消 (逻辑或关系). 父 context 的取消原因 (cause) 会自动传播到返回的 context.
//
// 新的 context 会继承:
// - Deadline: 所有父 context 中最早的截止时间.
// - Value: 按传入顺序从第一个能找到值的父 context 中获取值.
func MergeCtx(parents ...context.Context) (ctx context.Context, cancel context.CancelFunc) {
	if len(parents) == 0 {
		return context.WithCancel(context.Background())
	}
	if len(parents) == 1 {
		ctx, cancel := context.WithCancelCause(parents[0])
		return ctx, func() { cancel(nil) }
	}

	var earliestDeadline time.Time
	for _, p := range parents {
		if deadline, ok := p.Deadline(); ok {
			if earliestDeadline.IsZero() || deadline.Before(earliestDeadline) {
				earliestDeadline = deadline
			}
		}
	}

	// baseCtx 提供 CancelCauseFunc 以支持 cause 传播.
	baseCtx, baseCancel := context.WithCancelCause(context.Background())

	// deadlineCtx 仅用于监听 deadline 到期信号.
	var deadlineCtx context.Context
	var deadlineCancel context.CancelFunc
	if !earliestDeadline.IsZero() {
		deadlineCtx, deadlineCancel = context.WithDeadlineCause(context.Background(), earliestDeadline, context.DeadlineExceeded)
	}

	// 嵌入的 context: 有 deadline 时用 deadlineCtx, 否则用 baseCtx.
	embedCtx := baseCtx
	if deadlineCtx != nil {
		embedCtx = deadlineCtx
	}

	mc := &mergedContext{
		Context:     embedCtx,
		parents:     parents,
		cancelCtx:   baseCtx,
		deadlineCtx: deadlineCtx,
	}

	// 启动监控 goroutine.
	go func() {
		var once sync.Once
		doCancel := func(cause error) {
			once.Do(func() { baseCancel(cause) })
		}
		defer doCancel(nil)

		parentDone := orDone(mc.parents...)

		if deadlineCtx != nil {
			defer deadlineCancel()
			select {
			case <-parentDone:
				for _, p := range mc.parents {
					if p.Err() != nil {
						doCancel(context.Cause(p))
						return
					}
				}
				doCancel(nil)
			case <-deadlineCtx.Done():
				doCancel(context.DeadlineExceeded)
			case <-baseCtx.Done():
			}
		} else {
			select {
			case <-parentDone:
				for _, p := range mc.parents {
					if p.Err() != nil {
						doCancel(context.Cause(p))
						return
					}
				}
				doCancel(nil)
			case <-baseCtx.Done():
			}
		}
	}()

	return mc, func() { baseCancel(nil) }
}

// Value 返回当前Ctx Value. 先检查嵌入的 context (以支持 context.Cause),
// 再按传入顺序从 parents 中查找.
func (mc *mergedContext) Value(key any) any {
	if v := mc.Context.Value(key); v != nil {
		return v
	}
	for _, p := range mc.parents {
		if val := p.Value(key); val != nil {
			return val
		}
	}
	return nil
}

// Deadline 实现了 context.Context 的 Deadline 方法.
func (mc *mergedContext) Deadline() (deadline time.Time, ok bool) {
	return mc.Context.Deadline()
}

// Done 实现了 context.Context 的 Done 方法.
func (mc *mergedContext) Done() <-chan struct{} {
	if mc.deadlineCtx != nil {
		mc.doneOnce.Do(func() {
			mc.done = orDone(mc.cancelCtx, mc.deadlineCtx)
		})
		return mc.done
	}
	return mc.cancelCtx.Done()
}

// Err 实现了 context.Context 的 Err 方法.
func (mc *mergedContext) Err() error {
	if mc.cancelCtx.Err() != nil {
		return mc.cancelCtx.Err()
	}
	if mc.deadlineCtx != nil {
		return mc.deadlineCtx.Err()
	}
	return nil
}

// Cause 返回取消原因, 使 context.Cause() 能正确传播 cause.
func (mc *mergedContext) Cause() error {
	return context.Cause(mc.cancelCtx)
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
