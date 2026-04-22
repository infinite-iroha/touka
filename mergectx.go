// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"context"
	"time"
)

// mergedContext 实现了 context.Context 接口, 是 Merge 函数返回的实际类型.
// 嵌入 cancelCtx 作为基础 context, 支持 cause 传播.
// deadlineCtx 作为 cancelCtx 的子 context, 确保 deadline 到期时 cancelCtx 也被取消.
type mergedContext struct {
	context.Context
	parents []context.Context
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

	// cancelCtx 作为基础 context, 提供 CancelCauseFunc 以支持 cause 传播.
	cancelCtx, cancelCause := context.WithCancelCause(context.Background())

	// deadlineCtx 作为 cancelCtx 的子 context (如果有 deadline).
	// 当 cancelCtx 被取消时, deadlineCtx 也会被取消;
	// 当 deadline 到期时, deadlineCtx 自行取消, watcher 负责关闭 cancelCtx.
	var deadlineCtx context.Context
	var deadlineCancel context.CancelFunc
	if !earliestDeadline.IsZero() {
		deadlineCtx, deadlineCancel = context.WithDeadlineCause(cancelCtx, earliestDeadline, context.DeadlineExceeded)
	}

	// 嵌入的 context: 有 deadline 时用 deadlineCtx (以返回正确的 Deadline),
	// 否则用 cancelCtx.
	embedCtx := cancelCtx
	if deadlineCtx != nil {
		embedCtx = deadlineCtx
	}

	mc := &mergedContext{
		Context: embedCtx,
		parents: parents,
	}

	// 启动监控 goroutine, 监听 parent 取消或 deadline 到期.
	go func() {
		// 将 cancelCtx 加入 orDone, 确保手动 cancel() 时 orDone goroutine 能退出, 防止泄漏.
		parentDone := orDone(append(mc.parents, cancelCtx)...)

		if deadlineCtx != nil {
			defer deadlineCancel()
			select {
			case <-parentDone:
				// parent 取消或手动 cancel()
				for _, p := range mc.parents {
					if p.Err() != nil {
						cancelCause(context.Cause(p))
						return
					}
				}
				// 手动 cancel(), cause 已由 cancelCause() 设置
			case <-deadlineCtx.Done():
				// deadline 到期, 需要关闭 cancelCtx 并设置 cause
				cancelCause(context.DeadlineExceeded)
			}
		} else {
			<-parentDone
			for _, p := range mc.parents {
				if p.Err() != nil {
					cancelCause(context.Cause(p))
					return
				}
			}
		}
	}()

	return mc, func() { cancelCause(nil) }
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

// Deadline, Done, Err 均由嵌入的 context.Context 提供.

// orDone 返回一个 channel, 当任意一个输入 context 的 Done() channel 关闭时关闭.
func orDone(contexts ...context.Context) <-chan struct{} {
	done := make(chan struct{})
	for _, ctx := range contexts {
		go func(c context.Context) {
			select {
			case <-c.Done():
				select {
				case <-done:
				default:
					close(done)
				}
			case <-done:
			}
		}(ctx)
	}
	return done
}
