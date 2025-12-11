// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package webdav

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/infinite-iroha/touka"
)

func TestRegister(t *testing.T) {
	r := touka.New()
	cfg := &Config{
		FileSystem: NewMemFS(),
		LockSystem: NewMemLock(),
	}
	Register(r, "/dav", cfg)

	// Check if a WebDAV method is registered
	req, _ := http.NewRequest("PROPFIND", "/dav/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Errorf("Expected PROPFIND to be registered, but got 404")
	}
}

func TestServe(t *testing.T) {
	r := touka.New()
	dir, _ := os.MkdirTemp("", "webdav")
	defer os.RemoveAll(dir)

	closer, err := Serve(r, "/serve", dir)
	if err != nil {
		t.Fatalf("Serve failed: %v", err)
	}
	defer closer.Close()

	// Check if a WebDAV method is registered
	req, _ := http.NewRequest("OPTIONS", "/serve/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected OPTIONS to return 200, but got %d", w.Code)
	}
}
