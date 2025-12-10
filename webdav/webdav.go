// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
// Copyright 2024 WJQSERVER. All rights reserved.
// All rights reserved by WJQSERVER, related rights can be exercised by the infinite-iroha organization.
package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/infinite-iroha/touka"
)

// FileSystem defines the interface for a file system to be served by the WebDAV handler.
// It provides methods for file and directory manipulation and information retrieval,
// abstracting the underlying storage from the WebDAV protocol logic.
type FileSystem interface {
	Mkdir(ctx context.Context, name string, perm os.FileMode) error
	OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (File, error)
	RemoveAll(ctx context.Context, name string) error
	Rename(ctx context.Context, oldName, newName string) error
	Stat(ctx context.Context, name string) (ObjectInfo, error)
}

// File defines the interface for a file-like object in the FileSystem.
// It embeds standard io interfaces for reading, writing, seeking, and closing,
// and adds methods for directory listing and metadata retrieval.
type File interface {
	io.Closer
	io.Reader
	io.Seeker
	io.Writer
	Readdir(count int) ([]ObjectInfo, error)
	Stat() (ObjectInfo, error)
}

// ObjectInfo provides a common interface for file and directory metadata.
// It is designed to be compatible with os.FileInfo to allow for easy integration
// with standard library functions, while providing an abstraction layer.
type ObjectInfo interface {
	Name() string
	Size() int64
	Mode() os.FileMode
	ModTime() time.Time
	IsDir() bool
	Sys() interface{} // Underlying data source (can be nil).
}

// Propfind represents the XML structure of a PROPFIND request body.
// It allows clients to request all properties (`Allprop`), a specific set of
// properties (`Prop`), or just property names (`Propname`).
type Propfind struct {
	XMLName  xml.Name `xml:"DAV: propfind"`
	Allprop  *struct{} `xml:"DAV: allprop"`
	Prop     *Prop     `xml:"DAV: prop"`
	Propname *struct{} `xml:"DAV: propname"`
}

// Prop represents a container for specific properties requested or returned
// in PROPFIND and PROPPATCH methods. Each field corresponds to a DAV property.
type Prop struct {
	XMLName          xml.Name      `xml:"DAV: prop"`
	GetContentLength *string       `xml:"DAV: getcontentlength,omitempty"`
	GetLastModified  *string       `xml:"DAV: getlastmodified,omitempty"`
	GetContentType   *string       `xml:"DAV: getcontenttype,omitempty"`
	ResourceType     *ResourceType `xml:"DAV: resourcetype,omitempty"`
	CreationDate     *string       `xml:"DAV: creationdate,omitempty"`
	DisplayName      *string       `xml:"DAV: displayname,omitempty"`
	SupportedLock    *SupportedLock `xml:"DAV: supportedlock,omitempty"`
	LockDiscovery    *LockDiscovery `xml:"DAV: lockdiscovery,omitempty"`
}

// LockDiscovery contains information about the active locks on a resource.
type LockDiscovery struct {
	XMLName    xml.Name     `xml:"DAV: lockdiscovery"`
	ActiveLock []ActiveLock `xml:"DAV: activelock"`
}

// ActiveLock describes an active lock on a resource.
type ActiveLock struct {
	XMLName   xml.Name  `xml:"DAV: activelock"`
	LockType  LockType  `xml:"DAV: locktype"`
	LockScope LockScope `xml:"DAV: lockscope"`
	Depth     string    `xml:"DAV: depth"`
	Owner     Owner     `xml:"DAV: owner"`
	Timeout   string    `xml:"DAV: timeout"`
	LockToken *LockToken `xml:"DAV: locktoken,omitempty"`
}

// LockToken represents a lock token.
type LockToken struct {
	XMLName xml.Name `xml:"DAV: locktoken"`
	Href    string   `xml:"DAV: href"`
}

// ResourceType indicates the nature of a resource, typically whether it is
// a collection (directory) or a standard resource.
type ResourceType struct {
	XMLName    xml.Name    `xml:"DAV: resourcetype"`
	Collection *struct{}   `xml:"DAV: collection,omitempty"`
}

// SupportedLock defines the types of locks supported by a resource.
type SupportedLock struct {
	XMLName   xml.Name    `xml:"DAV: supportedlock"`
	LockEntry []LockEntry `xml:"DAV: lockentry"`
}

// LockEntry describes a single type of lock that is supported.
type LockEntry struct {
	XMLName   xml.Name  `xml:"DAV: lockentry"`
	LockScope LockScope `xml:"DAV: lockscope"`
	LockType  LockType  `xml:"DAV: locktype"`
}

// LockScope specifies whether a lock is exclusive or shared.
type LockScope struct {
	XMLName   xml.Name    `xml:"DAV: lockscope"`
	Exclusive *struct{}   `xml:"DAV: exclusive,omitempty"`
	Shared    *struct{}   `xml:"DAV: shared,omitempty"`
}

// LockType indicates the type of lock, typically a write lock.
type LockType struct {
	XMLName xml.Name  `xml:"DAV: locktype"`
	Write   *struct{} `xml:"DAV: write,omitempty"`
}

// Multistatus is the root element for responses to PROPFIND and PROPPATCH
// requests, containing multiple individual responses for different resources.
type Multistatus struct {
	XMLName   xml.Name    `xml:"DAV: multistatus"`
	Responses []*Response `xml:"DAV: response"`
}

// Response represents the status and properties of a single resource within
// a Multistatus response.
type Response struct {
	XMLName   xml.Name   `xml:"DAV: response"`
	Href      []string   `xml:"DAV: href"`
	Propstats []Propstat `xml:"DAV: propstat"`
}

// Propstat groups properties with their corresponding HTTP status in a
// single response, indicating success or failure for those properties.
type Propstat struct {
	XMLName xml.Name `xml:"DAV: propstat"`
	Prop    Prop     `xml:"DAV: prop"`
	Status  string   `xml:"DAV: status"`
}

// LockSystem is the interface for a lock manager.
type LockSystem interface {
	// Create creates a new lock.
	Create(ctx context.Context, path string, lockInfo LockInfo) (string, error)
	// Refresh refreshes an existing lock.
	Refresh(ctx context.Context, token string, timeout time.Duration) error
	// Unlock removes a lock.
	Unlock(ctx context.Context, token string) error
}

// Handler handles WebDAV requests.
type Handler struct {
	// Prefix is the URL prefix that the handler is mounted on.
	Prefix string
	// FileSystem is the file system that is served.
	FileSystem FileSystem
	// LockSystem is the lock system. If nil, locking is disabled.
	LockSystem LockSystem
	// Logger is the logger to use. If nil, logging is disabled.
	Logger Logger
}

// LockInfo contains information about a lock.
type LockInfo struct {
	XMLName   xml.Name  `xml:"DAV: lockinfo"`
	LockScope LockScope `xml:"DAV: lockscope"`
	LockType  LockType  `xml:"DAV: locktype"`
	Owner     Owner     `xml:"DAV: owner"`
	Timeout   time.Duration
}

// Owner represents the owner of a lock.
type Owner struct {
	XMLName xml.Name `xml:"DAV: owner"`
	Href    string   `xml:"DAV: href"`
}

// Logger is a simple logging interface.
type Logger interface {
	Printf(format string, v ...interface{})
}

// NewHandler returns a new Handler.
func NewHandler(prefix string, fs FileSystem, ls LockSystem, logger Logger) *Handler {
	return &Handler{
		Prefix:     prefix,
		FileSystem: fs,
		LockSystem: ls,
		Logger:     logger,
	}
}

// ServeTouka handles a Touka request.
func (h *Handler) ServeTouka(c *touka.Context) {
	path := h.stripPrefix(c.Request.URL.Path)
	c.Set("webdav_path", path)

	switch c.Request.Method {
	case "OPTIONS":
		h.handleOptions(c)
	case "GET", "HEAD":
		h.handleGetHead(c)
	case "DELETE":
		h.handleDelete(c)
	case "PUT":
		h.handlePut(c)
	case "MKCOL":
		h.handleMkcol(c)
	case "COPY":
		h.handleCopy(c)
	case "MOVE":
		h.handleMove(c)
	case "PROPFIND":
		h.handlePropfind(c)
	case "PROPPATCH":
		h.handleProppatch(c)
	case "LOCK":
		h.handleLock(c)
	case "UNLOCK":
		h.handleUnlock(c)
	default:
		c.Status(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleOptions(c *touka.Context) {
	allow := "OPTIONS, GET, HEAD, DELETE, PUT, MKCOL, COPY, MOVE, PROPFIND, PROPPATCH"
	dav := "1"
	if h.LockSystem != nil {
		allow += ", LOCK, UNLOCK"
		dav += ", 2"
	}

	c.SetHeader("Allow", allow)
	c.SetHeader("DAV", dav)
	c.Status(http.StatusOK)
}

func (h *Handler) handleGetHead(c *touka.Context) {
	path, _ := c.Get("webdav_path")
	file, err := h.FileSystem.OpenFile(c.Context(), path.(string), os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			c.Status(http.StatusNotFound)
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	if info.IsDir() {
		c.Status(http.StatusForbidden)
		return
	}

	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}

func (h *Handler) handleDelete(c *touka.Context) {
	path, _ := c.Get("webdav_path")
	pathStr := path.(string)

	info, err := h.FileSystem.Stat(c.Context(), pathStr)
	if err != nil {
		if os.IsNotExist(err) {
			c.Status(http.StatusNotFound)
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return
	}

	if info.IsDir() {
		file, err := h.FileSystem.OpenFile(c.Context(), pathStr, os.O_RDONLY, 0)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		defer file.Close()

		// Check if the directory has any children. Readdir(1) is enough.
		children, err := file.Readdir(1)
		if err != nil && err != io.EOF {
			c.Status(http.StatusInternalServerError)
			return
		}
		if len(children) > 0 {
			c.Status(http.StatusConflict) // 409 Conflict for non-empty collection
			return
		}
	}

	if err := h.FileSystem.RemoveAll(c.Context(), pathStr); err != nil {
		if os.IsNotExist(err) {
			c.Status(http.StatusNotFound)
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) handlePut(c *touka.Context) {
	path, _ := c.Get("webdav_path")
	file, err := h.FileSystem.OpenFile(c.Context(), path.(string), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	defer file.Close()

	if _, err := io.Copy(file, c.Request.Body); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	c.Status(http.StatusCreated)
}

func (h *Handler) handleMkcol(c *touka.Context) {
	path, _ := c.Get("webdav_path")
	if err := h.FileSystem.Mkdir(c.Context(), path.(string), 0755); err != nil {
		if os.IsExist(err) {
			c.Status(http.StatusMethodNotAllowed)
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return
	}
	c.Status(http.StatusCreated)
}

func (h *Handler) handleCopy(c *touka.Context) {
    srcPath, _ := c.Get("webdav_path")
    destPath := c.GetReqHeader("Destination")
    if destPath == "" {
        c.Status(http.StatusBadRequest)
        return
    }

    // A more complete implementation would parse the full URL.
    // For now, we assume the destination is a simple path.
    destURL, err := url.Parse(destPath)
    if err != nil {
        c.Status(http.StatusBadRequest)
        return
    }
    destPath = h.stripPrefix(destURL.Path)

    overwrite := c.GetReqHeader("Overwrite")
    if overwrite == "" {
        overwrite = "T" // Default is to overwrite
    }

    // Check for existence before the operation to determine status code later.
	_, err = h.FileSystem.Stat(c.Context(), destPath)
	existed := err == nil

    if overwrite == "F" && existed {
        c.Status(http.StatusPreconditionFailed)
        return
    }

    if err := h.copy(c.Context(), srcPath.(string), destPath); err != nil {
        c.Status(http.StatusInternalServerError)
        return
    }

	if existed {
		c.Status(http.StatusNoContent)
	} else {
		c.Status(http.StatusCreated)
	}
}

func (h *Handler) handleMove(c *touka.Context) {
    srcPath, _ := c.Get("webdav_path")
    destPath := c.GetReqHeader("Destination")
    if destPath == "" {
        c.Status(http.StatusBadRequest)
        return
    }

    destURL, err := url.Parse(destPath)
    if err != nil {
        c.Status(http.StatusBadRequest)
        return
    }
    destPath = h.stripPrefix(destURL.Path)

    overwrite := c.GetReqHeader("Overwrite")
    if overwrite == "" {
        overwrite = "T" // Default is to overwrite
    }

    // Check for existence before the operation to determine status code later.
	_, err = h.FileSystem.Stat(c.Context(), destPath)
	existed := err == nil

    if overwrite == "F" && existed {
        c.Status(http.StatusPreconditionFailed)
        return
    }

    if err := h.FileSystem.Rename(c.Context(), srcPath.(string), destPath); err != nil {
        c.Status(http.StatusInternalServerError)
        return
    }

	if existed {
		c.Status(http.StatusNoContent)
	} else {
		c.Status(http.StatusCreated)
	}
}

func (h *Handler) copy(ctx context.Context, src, dest string) error {
	info, err := h.FileSystem.Stat(ctx, src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if err := h.FileSystem.Mkdir(ctx, dest, info.Mode()); err != nil {
			return err
		}

		srcFile, err := h.FileSystem.OpenFile(ctx, src, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		children, err := srcFile.Readdir(0)
		if err != nil {
			return err
		}

		for _, child := range children {
			if err := h.copy(ctx, path.Join(src, child.Name()), path.Join(dest, child.Name())); err != nil {
				return err
			}
		}
		return nil
	}

	srcFile, err := h.FileSystem.OpenFile(ctx, src, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	destFile, err := h.FileSystem.OpenFile(ctx, dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, srcFile)
	return err
}

func (h *Handler) handlePropfind(c *touka.Context) {
	requestPath, _ := c.Get("webdav_path")
	info, err := h.FileSystem.Stat(c.Context(), requestPath.(string))
	if err != nil {
		if os.IsNotExist(err) {
			c.Status(http.StatusNotFound)
		} else {
			c.Status(http.StatusInternalServerError)
		}
		return
	}

	var propfind Propfind
	if c.Request.ContentLength != 0 {
		if err := xml.NewDecoder(c.Request.Body).Decode(&propfind); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
	}

	ms := &Multistatus{
		Responses: make([]*Response, 0),
	}

	depth := c.GetReqHeader("Depth")
	if depth == "" {
		depth = "infinity"
	}

	ms.Responses = append(ms.Responses, h.createPropfindResponse(requestPath.(string), info, propfind))

	if info.IsDir() && depth != "0" {
		var walk func(string, int) error
		walk = func(p string, maxDepth int) error {
			if maxDepth == 0 {
				return nil
			}

			file, err := h.FileSystem.OpenFile(c.Context(), p, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			defer file.Close()

			children, err := file.Readdir(0)
			if err != nil {
				return err
			}

			for _, child := range children {
				childPath := path.Join(p, child.Name())
				childInfo, err := h.FileSystem.Stat(c.Context(), childPath)
				if err != nil {
					if h.Logger != nil {
						h.Logger.Printf("PROPFIND walk: failed to stat child %s: %v", childPath, err)
					}
					continue
				}
				ms.Responses = append(ms.Responses, h.createPropfindResponse(childPath, childInfo, propfind))
				if childInfo.IsDir() {
					if err := walk(childPath, maxDepth-1); err != nil {
						return err
					}
				}
			}
			return nil
		}

		walkDepth := -1
		if depth == "1" {
			walkDepth = 1
		}

		if err := walk(requestPath.(string), walkDepth); err != nil {
			if h.Logger != nil {
				h.Logger.Printf("Error during PROPFIND walk: %v", err)
			}
			c.Status(http.StatusInternalServerError)
			return
		}
	}

	c.Writer.Header().Set("Content-Type", "application/xml; charset=utf-8")
	c.Status(http.StatusMultiStatus)
	if err := xml.NewEncoder(c.Writer).Encode(ms); err != nil {
		h.Logger.Printf("Error encoding propfind response: %v", err)
	}
}


func (h *Handler) createPropfindResponse(path string, info ObjectInfo, propfind Propfind) *Response {
	fullPath := path
	if h.Prefix != "/" {
		fullPath = h.Prefix + path
	}

	resp := &Response{
		Href:      []string{fullPath},
		Propstats: make([]Propstat, 0),
	}

	prop := Prop{}
	if propfind.Allprop != nil {
		prop.GetContentLength = new(string)
		*prop.GetContentLength = fmt.Sprintf("%d", info.Size())

		prop.GetLastModified = new(string)
		*prop.GetLastModified = info.ModTime().Format(http.TimeFormat)

		prop.ResourceType = &ResourceType{}
		if info.IsDir() {
			prop.ResourceType.Collection = &struct{}{}
		}
	} else if propfind.Prop != nil {
		if propfind.Prop.GetContentLength != nil {
			prop.GetContentLength = new(string)
			*prop.GetContentLength = fmt.Sprintf("%d", info.Size())
		}
		if propfind.Prop.GetLastModified != nil {
			prop.GetLastModified = new(string)
			*prop.GetLastModified = info.ModTime().Format(http.TimeFormat)
		}
		if propfind.Prop.ResourceType != nil {
			prop.ResourceType = &ResourceType{}
			if info.IsDir() {
				prop.ResourceType.Collection = &struct{}{}
			}
		}
	}

	resp.Propstats = append(resp.Propstats, Propstat{
		Prop:   prop,
		Status: "HTTP/1.1 200 OK",
	})

	return resp
}

func (h *Handler) handleProppatch(c *touka.Context) {
	c.Status(http.StatusNotImplemented)
}

func (h *Handler) stripPrefix(p string) string {
	if h.Prefix == "/" {
		return p
	}
	return strings.TrimPrefix(p, h.Prefix)
}

func (h *Handler) handleLock(c *touka.Context) {
	if h.LockSystem == nil {
		c.Status(http.StatusMethodNotAllowed)
		return
	}

	path, _ := c.Get("webdav_path")
	tokenHeader := c.GetReqHeader("If")
	var token string
	if tokenHeader != "" {
		// Basic parsing for <opaquelocktoken:c2134f...>
		if strings.HasPrefix(tokenHeader, "(<") && strings.HasSuffix(tokenHeader, ">)") {
			token = strings.TrimPrefix(tokenHeader, "(<")
			token = strings.TrimSuffix(token, ">)")
		}
	}

	// Refresh lock
	if token != "" {
		timeoutStr := c.GetReqHeader("Timeout")
		timeout, err := parseTimeout(timeoutStr)
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}

		if err := h.LockSystem.Refresh(c.Context(), token, timeout); err != nil {
			c.Status(http.StatusPreconditionFailed)
			return
		}
	} else {
		// Create lock
		var lockInfo LockInfo
		if err := xml.NewDecoder(c.Request.Body).Decode(&lockInfo); err != nil {
			c.Status(http.StatusBadRequest)
			return
		}

		timeoutStr := c.GetReqHeader("Timeout")
		timeout, err := parseTimeout(timeoutStr)
		if err != nil {
			c.Status(http.StatusBadRequest)
			return
		}
		lockInfo.Timeout = timeout

		token, err = h.LockSystem.Create(c.Context(), path.(string), lockInfo)
		if err != nil {
			c.Status(http.StatusConflict)
			return
		}
	}

	prop := Prop{
		LockDiscovery: &LockDiscovery{
			ActiveLock: []ActiveLock{
				{
					LockToken: &LockToken{Href: token},
				},
			},
		},
	}

	c.Writer.Header().Set("Content-Type", "application/xml; charset=utf-8")
	c.Writer.Header().Set("Lock-Token", token)
	c.Status(http.StatusOK)
	xml.NewEncoder(c.Writer).Encode(prop)
}

func parseTimeout(timeoutStr string) (time.Duration, error) {
	if timeoutStr == "" || strings.ToLower(timeoutStr) == "infinite" {
		// A long timeout, as per RFC 4918.
		return 10 * time.Minute, nil
	}
	// "Second-123"
	parts := strings.Split(timeoutStr, "-")
	if len(parts) == 2 && strings.ToLower(parts[0]) == "second" {
		seconds, err := time.ParseDuration(parts[1] + "s")
		if err == nil {
			return seconds, nil
		}
	}
	return 0, os.ErrInvalid
}

func (h *Handler) handleUnlock(c *touka.Context) {
	if h.LockSystem == nil {
		c.Status(http.StatusMethodNotAllowed)
		return
	}

	tokenHeader := c.GetReqHeader("Lock-Token")
	if tokenHeader == "" {
		c.Status(http.StatusBadRequest)
		return
	}

	// Basic parsing for <urn:uuid:f81d4fae...>
	token := strings.TrimPrefix(tokenHeader, "<")
	token = strings.TrimSuffix(token, ">")

	if err := h.LockSystem.Unlock(c.Context(), token); err != nil {
		c.Status(http.StatusConflict)
		return
	}

	c.Status(http.StatusNoContent)
}
