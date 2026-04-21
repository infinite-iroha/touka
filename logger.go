// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

// Logger 是日志接口，支持多种日志库实现（reco、zap、logrus 等）
// 用户可以通过实现此接口来替换默认的日志实现
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Panicf(format string, args ...any)
}

// CloserLogger 可选扩展接口，支持关闭操作
// 如果 Logger 实现了此接口，Engine 在关闭时会调用 Close()
type CloserLogger interface {
	Logger
	Close() error
}
