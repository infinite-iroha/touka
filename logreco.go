// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package touka

import (
	"log"
	"os"
	"time"

	"github.com/fenthope/reco"
)

// 默认LogReco配置
var defaultLogRecoConfig = reco.Config{
	Level:         reco.LevelInfo,
	Mode:          reco.ModeText,
	TimeFormat:    time.RFC3339,
	Output:        os.Stdout,
	Async:         true,
	DefaultFields: nil,
}

func NewLogger(logcfg reco.Config) *reco.Logger {
	logger, err := reco.New(logcfg)
	if err != nil {
		log.Printf("New Logreco Error: %s", err)
		return nil
	}
	return logger
}

func CloseLogger(logger *reco.Logger) {
	err := logger.Close()
	if err != nil {
		log.Printf("Close Logreco Error: %s", err)
		return
	}
}

func (engine *Engine) CloseLogger() {
	if engine.LogReco != nil {
		CloseLogger(engine.LogReco)
	}
}
