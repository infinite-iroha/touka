// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package webdav

import (
	"bytes"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/infinite-iroha/touka"
)

func setupTestServer(handler *Handler) *touka.Engine {
	r := touka.New()
	webdavMethods := []string{
		"OPTIONS", "GET", "HEAD", "DELETE", "PUT", "MKCOL", "COPY", "MOVE", "PROPFIND", "PROPPATCH",
	}
	r.HandleFunc(webdavMethods, "/*path", handler.ServeTouka)
	return r
}

func TestHandleOptions(t *testing.T) {
	fs := NewMemFS()
	handler := NewHandler("/", fs, NewMemLock(), nil)
	r := setupTestServer(handler)

	req, _ := http.NewRequest("OPTIONS", "/", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status %d; got %d", http.StatusOK, w.Code)
	}
	if w.Header().Get("DAV") != "1, 2" {
		t.Errorf("Expected DAV header '1, 2'; got '%s'", w.Header().Get("DAV"))
	}
	expectedAllow := "OPTIONS, GET, HEAD, DELETE, PUT, MKCOL, COPY, MOVE, PROPFIND, PROPPATCH, LOCK, UNLOCK"
	if w.Header().Get("Allow") != expectedAllow {
		t.Errorf("Expected Allow header '%s'; got '%s'", expectedAllow, w.Header().Get("Allow"))
	}
}

func TestHandleMkcol(t *testing.T) {
	fs := NewMemFS()
	handler := NewHandler("/", fs, NewMemLock(), nil)
	r := setupTestServer(handler)

	req, _ := http.NewRequest("MKCOL", "/testdir", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected status %d; got %d", http.StatusCreated, w.Code)
	}

	// Verify the directory was created
	info, err := fs.Stat(nil, "/testdir")
	if err != nil {
		t.Fatalf("fs.Stat failed: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("Expected '/testdir' to be a directory")
	}
}

func TestHandlePropfind(t *testing.T) {
	fs := NewMemFS()
	handler := NewHandler("/", fs, NewMemLock(), nil)
	r := setupTestServer(handler)

	// Create a test directory and a test file
	fs.Mkdir(nil, "/testdir", 0755)
	file, _ := fs.OpenFile(&touka.Context{Request: &http.Request{}}, "/testdir/testfile", os.O_CREATE|os.O_WRONLY, 0644)
	file.Write([]byte("test content"))
	file.Close()

	propfindBody := `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:">
  <D:allprop/>
</D:propfind>`
	req, _ := http.NewRequest("PROPFIND", "/testdir", bytes.NewBufferString(propfindBody))
	req.Header.Set("Depth", "1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusMultiStatus {
		t.Fatalf("Expected status %d; got %d", http.StatusMultiStatus, w.Code)
	}

	var ms Multistatus
	if err := xml.Unmarshal(w.Body.Bytes(), &ms); err != nil {
		t.Fatalf("Failed to unmarshal propfind response: %v", err)
	}

	if len(ms.Responses) != 2 {
		t.Fatalf("Expected 2 responses; got %d", len(ms.Responses))
	}

	// Note: The order of responses is not guaranteed.
	var dirResp, fileResp *Response
	for _, resp := range ms.Responses {
		if resp.Href[0] == "/testdir" {
			dirResp = resp
		} else if resp.Href[0] == "/testdir/testfile" {
			fileResp = resp
		}
	}

	if dirResp == nil {
		t.Fatal("Response for directory not found")
	}
	if fileResp == nil {
		t.Fatal("Response for file not found")
	}

	// Check directory properties
	if dirResp.Propstats[0].Prop.ResourceType.Collection == nil {
		t.Error("Directory should have a collection resourcetype")
	}

	// Check file properties
	if fileResp.Propstats[0].Prop.ResourceType.Collection != nil {
		t.Error("File should not have a collection resourcetype")
	}
	if *fileResp.Propstats[0].Prop.GetContentLength != "12" {
		t.Errorf("Expected content length 12; got %s", *fileResp.Propstats[0].Prop.GetContentLength)
	}
}

func TestHandlePutGetDelete(t *testing.T) {
	fs := NewMemFS()
	handler := NewHandler("/", fs, NewMemLock(), nil)
	r := setupTestServer(handler)

	// PUT
	putReq, _ := http.NewRequest("PUT", "/test.txt", bytes.NewBufferString("hello"))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusCreated {
		t.Errorf("PUT: expected status %d, got %d", http.StatusCreated, putRec.Code)
	}

	// GET
	getReq, _ := http.NewRequest("GET", "/test.txt", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Errorf("GET: expected status %d, got %d", http.StatusOK, getRec.Code)
	}
	if getRec.Body.String() != "hello" {
		t.Errorf("GET: expected body 'hello', got '%s'", getRec.Body.String())
	}

	// DELETE
	delReq, _ := http.NewRequest("DELETE", "/test.txt", nil)
	delRec := httptest.NewRecorder()
	r.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Errorf("DELETE: expected status %d, got %d", http.StatusNoContent, delRec.Code)
	}

	// Verify deletion
	_, err := fs.Stat(nil, "/test.txt")
	if !os.IsNotExist(err) {
		t.Errorf("File should have been deleted, but stat returned: %v", err)
	}
}

func TestHandleCopyMove(t *testing.T) {
	fs := NewMemFS()
	handler := NewHandler("/", fs, NewMemLock(), nil)
	r := setupTestServer(handler)

	// Create source file
	putReq, _ := http.NewRequest("PUT", "/src.txt", bytes.NewBufferString("copy me"))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)

	// COPY
	copyReq, _ := http.NewRequest("COPY", "/src.txt", nil)
	copyReq.Header.Set("Destination", "/dest.txt")
	copyRec := httptest.NewRecorder()
	r.ServeHTTP(copyRec, copyReq)
	if copyRec.Code != http.StatusCreated {
		t.Errorf("COPY: expected status %d, got %d", http.StatusCreated, copyRec.Code)
	}

	// Verify copy
	info, err := fs.Stat(nil, "/dest.txt")
	if err != nil {
		t.Fatalf("Stat on copied file failed: %v", err)
	}
	if info.Size() != int64(len("copy me")) {
		t.Errorf("Copied file has wrong size")
	}

	// MOVE
	moveReq, _ := http.NewRequest("MOVE", "/dest.txt", nil)
	moveReq.Header.Set("Destination", "/moved.txt")
	moveRec := httptest.NewRecorder()
	r.ServeHTTP(moveRec, moveReq)
	if moveRec.Code != http.StatusCreated {
		t.Errorf("MOVE: expected status %d, got %d", http.StatusCreated, moveRec.Code)
	}

	// Verify move
	if _, err := fs.Stat(nil, "/dest.txt"); !os.IsNotExist(err) {
		t.Error("Original file should have been removed after move")
	}
	if _, err := fs.Stat(nil, "/moved.txt"); err != nil {
		t.Error("Moved file not found")
	}
}
