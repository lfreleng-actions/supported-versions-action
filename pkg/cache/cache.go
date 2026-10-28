// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package cache keeps upstream responses on disk, so that one job
// fetches each source once.
//
// Each entry is one JSON file named by its key. Writers create a
// temporary file in the cache directory and rename it into place, so a
// reader never sees a partial entry and no lock file is needed. The
// cache never follows symbolic links: a linked directory is an error
// and a linked or irregular entry is a miss. Every entry operation goes
// through an os.Root opened on the directory New checked, so replacing
// the directory's pathname later cannot redirect reads or writes.
package cache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Environment variables that decide the cache directory.
const (
	// EnvDir names the cache directory explicitly.
	EnvDir = "SUPPORTED_VERSIONS_CACHE_DIR"
	// EnvGitHubActions is "true" on a GitHub Actions runner.
	EnvGitHubActions = "GITHUB_ACTIONS"
	// EnvRunnerTemp is the runner's per-job temporary directory.
	EnvRunnerTemp = "RUNNER_TEMP"
)

// dirName is the cache directory's name under a parent directory.
const dirName = "supported-versions"

// Directory and entry permissions.
const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
)

// Entry is one cached upstream response. It never holds credentials.
type Entry struct {
	// Method is the HTTP method of the request.
	Method string `json:"method"`
	// URL is the request URL.
	URL string `json:"url"`
	// Status is the HTTP status of the response.
	Status int `json:"status"`
	// ETag is the response's ETag header, if any.
	ETag string `json:"etag,omitempty"`
	// LastModified is the response's Last-Modified header, if any.
	LastModified string `json:"last_modified,omitempty"`
	// FetchedAt is when the response was fetched or last revalidated.
	FetchedAt time.Time `json:"fetched_at"`
	// Body is the response body.
	Body []byte `json:"body"`
}

// Cache is an on-disk cache of upstream responses. It is safe for
// concurrent use, including by several processes sharing a directory.
// Close releases it.
type Cache struct {
	dir  string
	root *os.Root
}

// Dir returns the cache directory to use. The first match wins:
// $SUPPORTED_VERSIONS_CACHE_DIR; then supported-versions under
// $RUNNER_TEMP when GITHUB_ACTIONS is "true" and RUNNER_TEMP is set;
// then supported-versions under os.UserCacheDir.
func Dir() (string, error) {
	if d := os.Getenv(EnvDir); d != "" {
		return d, nil
	}
	if os.Getenv(EnvGitHubActions) == "true" {
		if rt := os.Getenv(EnvRunnerTemp); rt != "" {
			return filepath.Join(rt, dirName), nil
		}
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cache directory: %w", err)
	}
	return filepath.Join(base, dirName), nil
}

// Open returns a cache in the directory Dir resolves.
func Open() (*Cache, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return New(dir)
}

// afterMkdir and beforeOpen run in New after it creates the directory
// and between its checks and the open. Tests set them to swap the
// directory inside those windows; otherwise they are nil.
var afterMkdir, beforeOpen func()

// New returns a cache in dir, creating it when absent, and sets the
// directory's mode to 0700. It is an error for dir to be a symbolic
// link or anything other than a directory. The cache holds the
// directory open until Close.
func New(dir string) (*Cache, error) {
	if dir == "" {
		return nil, errors.New("cache directory: empty path")
	}
	// Clean drops a trailing separator, which would make Lstat report
	// a linked directory's target rather than the link.
	dir = filepath.Clean(dir)
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		if err = os.MkdirAll(dir, dirMode); err != nil {
			return nil, fmt.Errorf("cache directory: %w", err)
		}
		if afterMkdir != nil {
			afterMkdir()
		}
		fi, err = os.Lstat(dir)
	}
	if err != nil {
		return nil, fmt.Errorf("cache directory: %w", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("cache directory %s: is a symbolic link", dir)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("cache directory %s: not a directory", dir)
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("cache directory: %w", err)
	}
	// Refuse a directory swapped between the check and the open.
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(fi, opened) {
		_ = root.Close() //nolint:errcheck // Already failing.
		return nil, fmt.Errorf("cache directory %s: changed while opening", dir)
	}
	// Set the mode through the handle, never the pathname, so that a
	// directory swapped for a link cannot pass the change to its
	// target. The umask may also have narrowed the creation mode.
	if err = root.Chmod(".", dirMode); err != nil {
		_ = root.Close() //nolint:errcheck // Already failing.
		return nil, fmt.Errorf("cache directory: %w", err)
	}
	return &Cache{dir: dir, root: root}, nil
}

// Close releases the cache directory. The cache is unusable afterwards.
func (c *Cache) Close() error {
	return c.root.Close()
}

// Path returns the cache's directory, as New received it after
// cleaning. Entry operations never resolve it again.
func (c *Cache) Path() string {
	return c.dir
}

// Key returns the cache key for a request: the hex SHA-256 of its
// method, URL and body. Each part is length-prefixed, so no two
// distinct triples share a key. Callers pass a canonical body, or nil
// for a GET, and never a credential.
func Key(method, url string, body []byte) string {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(method), []byte(url), body} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validKey reports whether key has the form Key returns, so that a key
// can never name a path outside the cache directory.
func validKey(key string) bool {
	if len(key) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}

// afterLstat runs in Get between the Lstat and the open of an entry.
// Tests set it to swap the entry inside that window; otherwise it is
// nil.
var afterLstat func()

// name returns the file name of key's entry within the directory.
func name(key string) string {
	return key + ".json"
}

// Get returns the entry stored under key. ok is false when there is
// none, or when it is unreadable, corrupt (including data after the
// entry), a symbolic link or not a regular file; the next Put replaces
// it.
func (c *Cache) Get(key string) (e Entry, ok bool) {
	if !validKey(key) {
		return Entry{}, false
	}
	n := name(key)
	fi, err := c.root.Lstat(n)
	if err != nil || !fi.Mode().IsRegular() {
		return Entry{}, false
	}
	if afterLstat != nil {
		afterLstat()
	}
	// The open does not block, so an entry swapped for a FIFO after
	// Lstat cannot stall the lookup before the checks below refuse it.
	f, err := c.root.OpenFile(n, openReadFlags, 0)
	if err != nil {
		return Entry{}, false
	}
	defer f.Close() //nolint:errcheck // Read-only; a close error loses nothing.
	// Refuse an entry swapped for a link or an irregular file between
	// Lstat and Open.
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(fi, opened) {
		return Entry{}, false
	}
	dec := json.NewDecoder(f)
	if err = dec.Decode(&e); err != nil {
		return Entry{}, false
	}
	// Anything after the entry, even another JSON value, is corruption.
	if err = dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return Entry{}, false
	}
	return e, true
}

// Put stores e under key. It writes a temporary file with mode 0600
// and renames it into place, so concurrent writers leave one complete
// entry.
func (c *Cache) Put(key string, e Entry) (err error) {
	if !validKey(key) {
		return fmt.Errorf("cache: invalid key %q", key)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	tmpName := ".tmp-" + key + "-" + rand.Text()
	tmp, err := c.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()            //nolint:errcheck // Already failing.
			_ = c.root.Remove(tmpName) //nolint:errcheck // Best-effort cleanup.
		}
	}()
	// The umask may have narrowed the creation mode; set it exactly.
	if err = tmp.Chmod(fileMode); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err = c.root.Rename(tmpName, name(key)); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	return nil
}
