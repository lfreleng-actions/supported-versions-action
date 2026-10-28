// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

//go:build unix

package cache

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// getWithin calls c.Get(key) and fails the test unless it returns
// within d. A blocked Get leaks its goroutine; the test fails anyway.
func getWithin(t *testing.T, c *Cache, key string, d time.Duration) bool {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		_, ok := c.Get(key)
		done <- ok
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(d):
		t.Fatalf("Get blocked for over %v", d)
		return false
	}
}

// replaceWithFIFO swaps the regular entry at path for a FIFO with no
// writer, which a blocking open for reading would wait on forever.
func replaceWithFIFO(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return syscall.Mkfifo(path, 0o600)
}

func TestFIFOEntryIsMiss(t *testing.T) {
	c := newCache(t)
	key := Key(methodGet, testURL, nil)
	if err := c.Put(key, Entry{Method: methodGet, URL: testURL}); err != nil {
		t.Fatal(err)
	}
	if err := replaceWithFIFO(entryPath(c, key)); err != nil {
		t.Skipf("FIFOs unavailable: %v", err)
	}
	if getWithin(t, c, key, 5*time.Second) {
		t.Error("FIFO entry reported a hit")
	}
}

func TestEntrySwappedForFIFOIsMiss(t *testing.T) {
	c := newCache(t)
	key := Key(methodGet, testURL, nil)
	if err := c.Put(key, Entry{Method: methodGet, URL: testURL}); err != nil {
		t.Fatal(err)
	}
	// Swap the entry after Get has seen a regular file, so only the
	// non-blocking open and the handle's checks stand in the way.
	var swapErr error
	afterLstat = func() { swapErr = replaceWithFIFO(entryPath(c, key)) }
	t.Cleanup(func() { afterLstat = nil })
	ok := getWithin(t, c, key, 5*time.Second)
	if swapErr != nil {
		t.Skipf("FIFOs unavailable: %v", swapErr)
	}
	if ok {
		t.Error("entry swapped for a FIFO reported a hit")
	}
}
