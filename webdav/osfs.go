// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package webdav

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/infinite-iroha/touka"
)

// OSFS is a WebDAV FileSystem that uses the local OS file system.
type OSFS struct {
	RootDir string
}

// NewOSFS creates a new OSFS.
func NewOSFS(rootDir string) (*OSFS, error) {
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	return &OSFS{RootDir: rootDir}, nil
}

func (fs *OSFS) resolve(name string) (string, error) {
	if filepath.IsAbs(name) || strings.Contains(name, "..") {
		return "", os.ErrPermission
	}

	path := filepath.Join(fs.RootDir, name)

	// Evaluate symlinks, but only if the path exists.
	if _, err := os.Lstat(path); err == nil {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
		// For non-existent paths (like for PUT or MKCOL), we can't EvalSymlinks the full path.
		// Instead, we resolve the parent and ensure it's within the root.
	} else {
		parentDir := filepath.Dir(path)
		if _, err := os.Stat(parentDir); err == nil {
			parentDir, err = filepath.EvalSymlinks(parentDir)
			if err != nil {
				return "", err
			}
			path = filepath.Join(parentDir, filepath.Base(path))
		}
	}

	if !strings.HasPrefix(path, fs.RootDir) {
		return "", os.ErrPermission
	}

	return path, nil
}

// Mkdir creates a directory.
func (fs *OSFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	path, err := fs.resolve(name)
	if err != nil {
		return err
	}
	return os.Mkdir(path, perm)
}

// osFile is a wrapper around os.File that implements the File interface.
type osFile struct {
	*os.File
}

// Stat returns the FileInfo structure describing file.
func (f *osFile) Stat() (ObjectInfo, error) {
	fi, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return fi, nil
}

// Readdir reads the contents of the directory associated with file and returns
// a slice of up to n FileInfo values, as would be returned by Lstat.
func (f *osFile) Readdir(count int) ([]ObjectInfo, error) {
	fi, err := f.File.Readdir(count)
	if err != nil {
		return nil, err
	}
	oi := make([]ObjectInfo, len(fi))
	for i := range fi {
		oi[i] = fi[i]
	}
	return oi, nil
}

// OpenFile opens a file.
func (fs *OSFS) OpenFile(c *touka.Context, name string, flag int, perm os.FileMode) (File, error) {
	path, err := fs.resolve(name)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return &osFile{f}, nil
}

// RemoveAll removes a file or directory.
func (fs *OSFS) RemoveAll(ctx context.Context, name string) error {
	path, err := fs.resolve(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// Rename renames a file.
func (fs *OSFS) Rename(ctx context.Context, oldName, newName string) error {
	oldPath, err := fs.resolve(oldName)
	if err != nil {
		return err
	}
	newPath, err := fs.resolve(newName)
	if err != nil {
		return err
	}
	return os.Rename(oldPath, newPath)
}

// Stat returns file info.
func (fs *OSFS) Stat(ctx context.Context, name string) (ObjectInfo, error) {
	path, err := fs.resolve(name)
	if err != nil {
		return nil, err
	}
	return os.Stat(path)
}
