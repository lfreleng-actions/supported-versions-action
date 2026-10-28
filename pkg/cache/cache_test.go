// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

const (
	testURL   = "https://example.org/a"
	methodGet = "GET"
	// runnerTemp stands in for $RUNNER_TEMP.
	runnerTemp = "/runner"
	// onActions is GITHUB_ACTIONS on a runner.
	onActions = "true"
)

func newCache(t *testing.T) *Cache {
	t.Helper()
	c, err := New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c
}

// unixModesOnly skips a test of permission bits, which Windows lacks.
func unixModesOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix permissions only")
	}
}

// entryPath is the pathname of key's entry in c, for tests that tamper
// with the directory behind the cache's back.
func entryPath(c *Cache, key string) string {
	return filepath.Join(c.Path(), name(key))
}

func TestKeySeparation(t *testing.T) {
	base := Key(methodGet, testURL, nil)
	if len(base) != 64 {
		t.Fatalf("Key length = %d, want 64", len(base))
	}
	if Key(methodGet, testURL, nil) != base {
		t.Error("Key is not deterministic")
	}
	for name, k := range map[string]string{
		"method": Key("POST", testURL, nil),
		"url":    Key(methodGet, testURL+"b", nil),
		"body":   Key(methodGet, testURL, []byte("x")),
	} {
		if k == base {
			t.Errorf("changing the %s does not change the key", name)
		}
	}
	if Key("a", "bc", nil) == Key("ab", "c", nil) {
		t.Error(`("a","bc") and ("ab","c") collide`)
	}
	if Key("a", "", []byte("b")) == Key("a", "b", nil) {
		t.Error("URL and body collide")
	}
}

func TestRoundTrip(t *testing.T) {
	c := newCache(t)
	key := Key(methodGet, testURL, nil)
	if _, ok := c.Get(key); ok {
		t.Fatal("Get on an empty cache reported a hit")
	}
	want := Entry{
		Method:       methodGet,
		URL:          testURL,
		Status:       200,
		ETag:         `"abc"`,
		LastModified: "Wed, 28 Oct 2026 02:11:39 GMT",
		FetchedAt:    time.Date(2026, 10, 28, 2, 11, 39, 0, time.UTC),
		Body:         []byte(`{"ok":true}`),
	}
	if err := c.Put(key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("Get after Put reported a miss")
	}
	if got.Method != want.Method || got.URL != want.URL || got.Status != want.Status ||
		got.ETag != want.ETag || got.LastModified != want.LastModified ||
		!got.FetchedAt.Equal(want.FetchedAt) || !bytes.Equal(got.Body, want.Body) {
		t.Errorf("Get = %+v, want %+v", got, want)
	}
}

func TestInvalidKey(t *testing.T) {
	c := newCache(t)
	if err := c.Put("../escape", Entry{}); err == nil {
		t.Error("Put accepted a key that is not a hex digest")
	}
	if _, ok := c.Get("../escape"); ok {
		t.Error("Get accepted a key that is not a hex digest")
	}
}

func TestCorruptEntryIsMiss(t *testing.T) {
	valid, err := json.Marshal(Entry{Method: methodGet, URL: testURL})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"truncated":        []byte(`{"method":`),
		"trailing garbage": append(bytes.Clone(valid), "garbage"...),
		"second value":     append(bytes.Clone(valid), valid...),
	} {
		t.Run(name, func(t *testing.T) {
			c := newCache(t)
			key := Key(methodGet, testURL, nil)
			if err := os.WriteFile(entryPath(c, key), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok := c.Get(key); ok {
				t.Fatal("corrupt entry reported a hit")
			}
			if err := c.Put(key, Entry{Method: methodGet, URL: testURL}); err != nil {
				t.Fatalf("Put over a corrupt entry: %v", err)
			}
			if _, ok := c.Get(key); !ok {
				t.Error("Put did not replace the corrupt entry")
			}
		})
	}
}

func TestTrailingWhitespaceIsHit(t *testing.T) {
	c := newCache(t)
	key := Key(methodGet, testURL, nil)
	valid, err := json.Marshal(Entry{Method: methodGet, URL: testURL})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(entryPath(c, key), append(valid, " \n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(key); !ok {
		t.Error("entry followed by whitespace reported a miss")
	}
}

func TestSymlinkedDirectoryRefused(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, dir := range []string{link, link + string(filepath.Separator)} {
		if c, err := New(dir); err == nil {
			t.Cleanup(func() {
				if err := c.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			t.Errorf("New(%q) accepted a symbolically linked directory", dir)
		}
	}
}

func TestDirectorySwappedAfterNew(t *testing.T) {
	c := newCache(t)
	key := Key(methodGet, testURL, nil)
	if err := c.Put(key, Entry{Method: methodGet, URL: "original"}); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(c.Path())
	if err := os.Rename(c.Path(), filepath.Join(parent, "moved")); err != nil {
		t.Skipf("cannot rename an open directory: %v", err)
	}
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	planted := Entry{Method: methodGet, URL: "planted"}
	data, err := json.Marshal(planted)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(target, name(key)), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, c.Path()); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, ok := c.Get(key); ok && got.URL == planted.URL {
		t.Error("Get read an entry through the replacement link")
	}
	other := Key(methodGet, testURL+"b", nil)
	if err = c.Put(other, Entry{Method: methodGet, URL: testURL}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	left, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Name() != name(key) {
		t.Errorf("Put wrote through the replacement link: %v", left)
	}
	if _, err = os.Lstat(filepath.Join(parent, "moved", name(other))); err != nil {
		t.Errorf("Put did not write into the directory New opened: %v", err)
	}
}

func TestDirectorySwappedDuringNew(t *testing.T) {
	unixModesOnly(t)
	for name, hook := range map[string]*func(){
		"after mkdir": &afterMkdir,
		"before open": &beforeOpen,
	} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "cache")
			target := filepath.Join(parent, "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			// Set the mode explicitly, so the umask cannot narrow it.
			if err := os.Chmod(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if name == "before open" {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			var swapErr error
			*hook = func() {
				if swapErr = os.Remove(dir); swapErr == nil {
					swapErr = os.Symlink(target, dir)
				}
			}
			t.Cleanup(func() { *hook = nil })
			c, err := New(dir)
			if swapErr != nil {
				t.Skipf("cannot swap the directory: %v", swapErr)
			}
			if err == nil {
				_ = c.Close() //nolint:errcheck // Already failing.
				t.Error("New accepted a directory swapped for a link")
			}
			fi, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.Mode().Perm(); got != 0o755 {
				t.Errorf("link target mode = %o, want 755", got)
			}
		})
	}
}

func TestNewTightensExistingDirectory(t *testing.T) {
	unixModesOnly(t)
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err = c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != dirMode {
		t.Errorf("directory mode = %o, want %o", got, dirMode)
	}
}

func TestNotADirectoryRefused(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file); err == nil {
		t.Error("New accepted a regular file")
	}
	if _, err := New(""); err == nil {
		t.Error("New accepted an empty path")
	}
}

func TestSymlinkedEntryIsMiss(t *testing.T) {
	c := newCache(t)
	other := newCache(t)
	key := Key(methodGet, testURL, nil)
	if err := other.Put(key, Entry{Method: methodGet, URL: testURL}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(entryPath(other, key), entryPath(c, key)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, ok := c.Get(key); ok {
		t.Error("symbolically linked entry reported a hit")
	}
}

func TestIrregularEntryIsMiss(t *testing.T) {
	c := newCache(t)
	key := Key(methodGet, testURL, nil)
	if err := os.Mkdir(entryPath(c, key), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(key); ok {
		t.Error("directory entry reported a hit")
	}
}

func TestFileModes(t *testing.T) {
	unixModesOnly(t)
	c := newCache(t)
	fi, err := os.Stat(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != dirMode {
		t.Errorf("directory mode = %o, want %o", got, dirMode)
	}
	key := Key(methodGet, testURL, nil)
	if err = c.Put(key, Entry{Method: methodGet, URL: testURL}); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(entryPath(c, key))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != fileMode {
		t.Errorf("entry mode = %o, want %o", got, fileMode)
	}
}

func TestConcurrentWriters(t *testing.T) {
	c := newCache(t)
	key := Key(methodGet, testURL, nil)
	const writers = 16
	bodies := make(map[string]bool, writers)
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		body := bytes.Repeat([]byte(fmt.Sprintf("writer-%02d;", i)), 50_000)
		bodies[string(body)] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.Put(key, Entry{Method: methodGet, URL: testURL, Body: body})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Put: %v", err)
		}
	}
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("no complete entry after concurrent writes")
	}
	if !bodies[string(got.Body)] {
		t.Error("entry body matches no single writer")
	}
	left, err := filepath.Glob(filepath.Join(c.Path(), ".tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

func TestDirResolution(t *testing.T) {
	userCache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", userCache)
	t.Setenv("HOME", userCache)
	t.Setenv("LocalAppData", userCache)
	base, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no user cache directory: %v", err)
	}
	tests := []struct {
		name                       string
		explicit, actions, runnerT string
		want                       string
	}{
		{"explicit wins", "/explicit", onActions, runnerTemp, "/explicit"},
		{"runner temp on actions", "", onActions, runnerTemp, filepath.Join(runnerTemp, dirName)},
		{"runner temp ignored off actions", "", "false", runnerTemp, filepath.Join(base, dirName)},
		{"actions without runner temp", "", onActions, "", filepath.Join(base, dirName)},
		{"user cache fallback", "", "", "", filepath.Join(base, dirName)},
	}
	for _, tt := range tests {
		t.Setenv(EnvDir, tt.explicit)
		t.Setenv(EnvGitHubActions, tt.actions)
		t.Setenv(EnvRunnerTemp, tt.runnerT)
		got, err := Dir()
		if err != nil {
			t.Errorf("%s: Dir: %v", tt.name, err)
			continue
		}
		if got != tt.want {
			t.Errorf("%s: Dir = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "opened")
	t.Setenv(EnvDir, dir)
	c, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if c.Path() != dir {
		t.Errorf("Path = %q, want %q", c.Path(), dir)
	}
}
