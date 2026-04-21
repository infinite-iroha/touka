// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"github.com/WJQSERVER-STUDIO/httpc"
	"github.com/fenthope/reco"
)

// --- reco 兼容函数 ---

// GetLogReco 返回底层的 reco.Logger 实例
// 用于需要访问 reco 特定功能的场景
// 如果当前 logger 不是 *reco.Logger 类型，返回 nil
//
//go:fix inline
func (engine *Engine) GetLogReco() *reco.Logger {
	return engine.LogReco
}

// SetLogReco 设置 reco.Logger 实例
// 用于向后兼容，等价于 SetLogger(l)
//
//go:fix inline
func (engine *Engine) SetLogReco(l *reco.Logger) {
	engine.LogReco = l
	engine.logger = l
}

// GetLoggerReco 返回底层的 reco.Logger 实例
// 用于需要访问 reco 特定功能的场景
// 如果当前 logger 不是 *reco.Logger 类型，返回 nil
//
//go:fix inline
func (c *Context) GetLoggerReco() *reco.Logger {
	if rl, ok := c.engine.logger.(*reco.Logger); ok {
		return rl
	}
	return c.engine.LogReco
}

// --- httpc 兼容函数 ---

// GetHTTPC 返回底层的 httpc.Client 实例
// Deprecated: 使用 HTTPClient() 替代，新方法会自动关联请求 Context
//
//go:fix inline
func (c *Context) GetHTTPC() *httpc.Client {
	return c.Client()
}
