// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package webdav

import (
	"log"
	"os"

	"github.com/infinite-iroha/touka"
)

// Config is a configuration for the WebDAV handler.
type Config struct {
	FileSystem FileSystem
	LockSystem LockSystem
	Logger     Logger
}

// Register registers a WebDAV handler on the given router.
func Register(engine *touka.Engine, prefix string, cfg *Config) {
	if cfg.LockSystem == nil {
		cfg.LockSystem = NewMemLock()
	}

	handler := NewHandler(prefix, cfg.FileSystem, cfg.LockSystem, cfg.Logger)

	webdavMethods := []string{
		"OPTIONS", "GET", "HEAD", "DELETE", "PUT", "MKCOL", "COPY", "MOVE", "PROPFIND", "PROPPATCH", "LOCK", "UNLOCK",
	}
	engine.HandleFunc(webdavMethods, prefix+"/*path", handler.ServeTouka)
}

// Serve serves a local directory via WebDAV.
func Serve(engine *touka.Engine, prefix string, rootDir string) error {
	fs, err := NewOSFS(rootDir)
	if err != nil {
		return err
	}

	cfg := &Config{
		FileSystem: fs,
		Logger:     log.New(os.Stdout, "", 0),
	}
	Register(engine, prefix, cfg)
	return nil
}
