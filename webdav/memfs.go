// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package webdav

import (
	"context"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"
)

// MemFS is an in-memory file system for WebDAV using a tree structure.
type MemFS struct {
	mu   sync.RWMutex
	root *memNode
}

// NewMemFS creates a new in-memory file system.
func NewMemFS() *MemFS {
	return &MemFS{
		root: &memNode{
			name:     "/",
			isDir:    true,
			modTime:  time.Now(),
			children: make(map[string]*memNode),
		},
	}
}

// findNode traverses the tree to find a node by path.
func (fs *MemFS) findNode(path string) (*memNode, error) {
	current := fs.root
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" {
			continue
		}
		if current.children == nil {
			return nil, os.ErrNotExist
		}
		child, ok := current.children[part]
		if !ok {
			return nil, os.ErrNotExist
		}
		current = child
	}
	return current, nil
}

// Mkdir creates a directory in the in-memory file system.
func (fs *MemFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, base := path.Split(name)
	parent, err := fs.findNode(dir)
	if err != nil {
		return err
	}

	if _, exists := parent.children[base]; exists {
		return os.ErrExist
	}

	newNode := &memNode{
		name:     base,
		isDir:    true,
		modTime:  time.Now(),
		mode:     perm,
		parent:   parent,
		children: make(map[string]*memNode),
	}
	parent.children[base] = newNode
	return nil
}

// OpenFile opens a file in the in-memory file system.
func (fs *MemFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (File, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, base := path.Split(name)
	parent, err := fs.findNode(dir)
	if err != nil {
		return nil, err
	}

	node, exists := parent.children[base]
	if !exists {
		if flag&os.O_CREATE == 0 {
			return nil, os.ErrNotExist
		}
		node = &memNode{
			name:    base,
			modTime: time.Now(),
			mode:    perm,
			parent:  parent,
		}
		parent.children[base] = node
	}

	if flag&os.O_TRUNC != 0 {
		node.data = nil
	}

	return &memFile{
		node:     node,
		fs:       fs,
		offset:   0,
		fullPath: name,
	}, nil
}

// RemoveAll removes a file or directory from the in-memory file system.
func (fs *MemFS) RemoveAll(ctx context.Context, name string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	dir, base := path.Split(name)
	parent, err := fs.findNode(dir)
	if err != nil {
		return err
	}

	if _, exists := parent.children[base]; !exists {
		return os.ErrNotExist
	}

	delete(parent.children, base)
	return nil
}

// Rename renames a file in the in-memory file system.
func (fs *MemFS) Rename(ctx context.Context, oldName, newName string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	oldDir, oldBase := path.Split(oldName)
	newDir, newBase := path.Split(newName)

	oldParent, err := fs.findNode(oldDir)
	if err != nil {
		return err
	}

	node, exists := oldParent.children[oldBase]
	if !exists {
		return os.ErrNotExist
	}

	newParent, err := fs.findNode(newDir)
	if err != nil {
		return err
	}

	if _, exists := newParent.children[newBase]; exists {
		return os.ErrExist
	}

	delete(oldParent.children, oldBase)
	node.name = newBase
	node.parent = newParent
	newParent.children[newBase] = node
	return nil
}

// Stat returns the file info for a file or directory.
func (fs *MemFS) Stat(ctx context.Context, name string) (ObjectInfo, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.findNode(name)
}

type memNode struct {
	name     string
	isDir    bool
	size     int64
	modTime  time.Time
	mode     os.FileMode
	data     []byte
	parent   *memNode
	children map[string]*memNode
}

func (n *memNode) Name() string       { return n.name }
func (n *memNode) Size() int64        { return n.size }
func (n *memNode) Mode() os.FileMode  { return n.mode }
func (n *memNode) ModTime() time.Time { return n.modTime }
func (n *memNode) IsDir() bool        { return n.isDir }
func (n *memNode) Sys() interface{}   { return nil }

type memFile struct {
	node     *memNode
	fs       *MemFS
	offset   int64
	fullPath string
}

func (f *memFile) Close() error               { return nil }
func (f *memFile) Stat() (ObjectInfo, error) { return f.node, nil }

func (f *memFile) Read(p []byte) (n int, err error) {
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()
	if f.offset >= int64(len(f.node.data)) {
		return 0, io.EOF
	}
	n = copy(p, f.node.data[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *memFile) Write(p []byte) (n int, err error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.offset+int64(len(p)) > int64(len(f.node.data)) {
		newSize := f.offset + int64(len(p))
		newData := make([]byte, newSize)
		copy(newData, f.node.data)
		f.node.data = newData
	}
	n = copy(f.node.data[f.offset:], p)
	f.offset += int64(n)
	if f.offset > f.node.size {
		f.node.size = f.offset
	}
	return n, nil
}

func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	switch whence {
	case 0:
		f.offset = offset
	case 1:
		f.offset += offset
	case 2:
		f.offset = int64(len(f.node.data)) + offset
	}
	return f.offset, nil
}

// Readdir reads the contents of the directory associated with file and returns
// a slice of up to n FileInfo values, as would be returned by Lstat.
func (f *memFile) Readdir(count int) ([]ObjectInfo, error) {
	f.fs.mu.RLock()
	defer f.fs.mu.RUnlock()

	if !f.node.isDir {
		return nil, os.ErrInvalid
	}

	var infos []ObjectInfo
	for _, child := range f.node.children {
		infos = append(infos, child)
	}
	return infos, nil
}
