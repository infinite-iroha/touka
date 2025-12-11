// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package webdav

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"time"
)

// MemLock is an in-memory lock system for WebDAV.
type MemLock struct {
	mu    sync.RWMutex
	locks map[string]*lock
	stop  chan struct{}
}

type lock struct {
	token   string
	path    string
	expires time.Time
	info    LockInfo
}

// NewMemLock creates a new in-memory lock system.
func NewMemLock() *MemLock {
	l := &MemLock{
		locks: make(map[string]*lock),
		stop:  make(chan struct{}),
	}
	go l.cleanup()
	return l
}

// Close stops the cleanup goroutine.
func (l *MemLock) Close() error {
	close(l.stop)
	return nil
}

func (l *MemLock) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			for token, lock := range l.locks {
				if time.Now().After(lock.expires) {
					delete(l.locks, token)
				}
			}
			l.mu.Unlock()
		case <-l.stop:
			return
		}
	}
}

// Create creates a new lock.
func (l *MemLock) Create(ctx context.Context, path string, info LockInfo) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}
	tokenStr := hex.EncodeToString(token)

	l.locks[tokenStr] = &lock{
		token:   tokenStr,
		path:    path,
		expires: time.Now().Add(info.Timeout),
		info:    info,
	}
	return tokenStr, nil
}

// Refresh refreshes an existing lock.
func (l *MemLock) Refresh(ctx context.Context, token string, timeout time.Duration) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if lock, ok := l.locks[token]; ok {
		lock.expires = time.Now().Add(timeout)
		return nil
	}
	return os.ErrNotExist
}

// Unlock removes a lock.
func (l *MemLock) Unlock(ctx context.Context, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.locks, token)
	return nil
}
