// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package source

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
)

const (
	postToken = "Bearer secret-token-value"
	limited   = `{"errors":"rate limited"}`
	queryA    = `{"query":"a"}`
	queryB    = `{"query":"b"}`
)

// echoHandler answers a POST carrying postToken with its own body, and
// fails the test on anything else.
func echoHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != postToken {
			t.Errorf("Authorization = %q, want %q", got, postToken)
		}
		if got := r.Header.Get("User-Agent"); got != UserAgent {
			t.Errorf("User-Agent = %q, want %q", got, UserAgent)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the request: %v", err)
		}
		write(t, w, string(body))
	}
}

func authHeader() http.Header {
	return http.Header{"Authorization": {postToken}}
}

func TestPostCachesByBody(t *testing.T) {
	hs := newHarness(t, echoHandler(t))
	f := hs.fetcher(Options{})
	ctx := t.Context()

	res, err := f.Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)
	res, err = f.Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginCache, queryA)
	res, err = f.Post(ctx, hs.url(), []byte(queryB), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryB)
	if n := hs.hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if _, ok := hs.cache.Get(cache.Key(http.MethodGet, hs.url(), nil)); ok {
		t.Error("a POST response is cached under the GET key")
	}
}

func TestPostKeepsCredentialsOutOfTheCache(t *testing.T) {
	hs := newHarness(t, echoHandler(t))
	f := hs.fetcher(Options{})
	res, err := f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)

	dir := hs.cache.Path()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no cache entry written")
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(strings.TrimPrefix(postToken, "Bearer "))) {
			t.Errorf("cache file %s holds the credential", e.Name())
		}
	}
}

func TestPostCheckRejects(t *testing.T) {
	hs := newHarness(t, echoHandler(t))
	f := hs.fetcher(Options{Retries: 3})
	errBad := errors.New("bad payload")
	reject := func([]byte) error { return errBad }

	for range 2 {
		_, err := f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), reject)
		if !errors.Is(err, errBad) {
			t.Fatalf("err = %v, want the check's error", err)
		}
	}
	// Neither retried nor cached: each Post makes one request.
	if n := hs.hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestPostOffline(t *testing.T) {
	hs := newHarness(t, failHandler(t))
	f := hs.fetcher(Options{Offline: true})
	if !f.Offline() {
		t.Error("Offline() = false, want true")
	}
	if _, err := f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), nil); !errors.Is(err, ErrNoData) {
		t.Errorf("err = %v, want ErrNoData", err)
	}
	if hs.fetcher(Options{}).Offline() {
		t.Error("Offline() = true, want false")
	}
}

func TestPostCheckRejectsCachedEntry(t *testing.T) {
	hs := newHarness(t, echoHandler(t))
	f := hs.fetcher(Options{})
	ctx := t.Context()
	errBad := errors.New("bad payload")
	reject := func([]byte) error { return errBad }

	res, err := f.Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)
	// The fresh entry is not served to a caller whose check rejects
	// it: the Post fetches afresh, and the check rejects that too.
	if _, err = f.Post(ctx, hs.url(), []byte(queryA), authHeader(), reject); !errors.Is(err, errBad) {
		t.Fatalf("err = %v, want the check's error", err)
	}
	if n := hs.hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	// The rejected response replaced nothing.
	res, err = f.Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginCache, queryA)

	offline := hs.fetcher(Options{Offline: true})
	if _, err = offline.Post(ctx, hs.url(), []byte(queryA), authHeader(), reject); !errors.Is(err, ErrNoData) {
		t.Errorf("offline err = %v, want ErrNoData", err)
	}
}

// transient rejects the limited body as ErrTransient, and accepts any
// other.
func transient(b []byte) error {
	if string(b) == limited {
		return fmt.Errorf("%w: rate limited", ErrTransient)
	}
	return nil
}

// limitedThen answers while answered, counting requests, is at most n
// with the limited body and a Retry-After of 7 seconds, and after that
// echoes the request.
func limitedThen(t *testing.T, n int32, answered *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if answered.Add(1) <= n {
			w.Header().Set("Retry-After", "7")
			write(t, w, limited)
			return
		}
		echoHandler(t)(w, r)
	}
}

func TestPostCheckTransientRetries(t *testing.T) {
	var answered atomic.Int32
	hs := newHarness(t, limitedThen(t, 2, &answered))
	f := hs.fetcher(Options{Retries: 3})

	res, err := f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), transient)
	checkResult(t, res, err, OriginLive, queryA)
	if n := hs.hits.Load(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
	if len(hs.sleeps) != 2 || hs.sleeps[0] != 7*time.Second {
		t.Errorf("sleeps = %v, want two honoring Retry-After", hs.sleeps)
	}
}

func TestPostCheckTransientFallsBack(t *testing.T) {
	var answered atomic.Int32
	hs := newHarness(t, limitedThen(t, 0, &answered))
	f := hs.fetcher(Options{Retries: 2})
	ctx := t.Context()
	res, err := f.Post(ctx, hs.url(), []byte(queryA), authHeader(), transient)
	checkResult(t, res, err, OriginLive, queryA)

	// The upstream is rate limited from now on: an expired entry is
	// served stale after the retries, and never replaced.
	answered.Store(-100)
	hs.now = hs.now.Add(DefaultMaxAge + time.Hour)
	res, err = f.Post(ctx, hs.url(), []byte(queryA), authHeader(), transient)
	checkResult(t, res, err, OriginStale, queryA)
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "rate limited") {
		t.Errorf("warnings = %q, want one naming the rate limit", res.Warnings)
	}
	if n := hs.hits.Load(); n != 4 {
		t.Errorf("requests = %d, want 1 then 3", n)
	}
	if e, ok := hs.cache.Get(cache.Key(http.MethodPost, hs.url(), []byte(queryA))); !ok || string(e.Body) != queryA {
		t.Errorf("cached entry = %+v, %v; want the first response", e, ok)
	}

	// With no entry, the Post finds no data.
	_, err = f.Post(ctx, hs.url(), []byte(queryB), authHeader(), transient)
	if !errors.Is(err, ErrNoData) || !errors.Is(err, ErrTransient) {
		t.Errorf("err = %v, want ErrNoData wrapping ErrTransient", err)
	}
}

func TestPostSharedTransientRejection(t *testing.T) {
	limitedCheck := func([]byte) error { return ErrTransient }
	_, res, errs := sharedPosts(t, echoHandler(t), Options{}, nil, limitedCheck)
	checkResult(t, res[0], errs[0], OriginLive, queryA)
	if !errors.Is(errs[1], ErrNoData) || !errors.Is(errs[1], ErrTransient) {
		t.Errorf("err = %v, want ErrNoData wrapping ErrTransient", errs[1])
	}
}

// TestPostSharedTransientStarter starts a shared Post whose check finds
// every answer rate limited: once its retries run out, a caller whose
// check accepts the body is served the last answer, uncached.
func TestPostSharedTransientStarter(t *testing.T) {
	limitedCheck := func([]byte) error { return fmt.Errorf("%w: rate limited", ErrTransient) }
	arrived := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			close(arrived)
			<-release
		})
		echoHandler(t)(w, r)
	})
	res, errs := sharePosts(t, hs, Options{}, arrived, release, limitedCheck, nil)

	if !errors.Is(errs[0], ErrNoData) || !errors.Is(errs[0], ErrTransient) {
		t.Errorf("starter err = %v, want ErrNoData wrapping ErrTransient", errs[0])
	}
	checkResult(t, res[1], errs[1], OriginLive, queryA)
	if n := hs.hits.Load(); n != DefaultRetries+1 {
		t.Errorf("requests = %d, want %d", n, DefaultRetries+1)
	}
	if _, ok := hs.cache.Get(cache.Key(http.MethodPost, hs.url(), []byte(queryA))); ok {
		t.Error("a rejected answer was cached")
	}
}

// TestPostDoesNotRevalidate re-sends an expired POST unconditionally:
// a server holding a matching ETag answers a conditional POST with 412
// (RFC 9110), which would fail the Post instead of refreshing it.
func TestPostDoesNotRevalidate(t *testing.T) {
	var hs *harness
	hs = newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		w.Header().Set("ETag", etagV1)
		w.Header().Set("Last-Modified", "Wed, 28 Oct 2026 12:00:00 GMT")
		answer := queryA
		if hs.hits.Load() > 1 {
			answer = queryB
		}
		write(t, w, answer)
	})
	f := hs.fetcher(Options{})
	ctx := t.Context()

	res, err := f.Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)
	key := cache.Key(http.MethodPost, hs.url(), []byte(queryA))
	if e, ok := hs.cache.Get(key); !ok || e.ETag != etagV1 {
		t.Fatalf("cached entry = %+v, %v; want one with ETag %q", e, ok, etagV1)
	}

	hs.now = hs.now.Add(DefaultMaxAge + time.Hour)
	res, err = f.Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryB)
	if n := hs.hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if e, ok := hs.cache.Get(key); !ok || string(e.Body) != queryB || !e.FetchedAt.Equal(hs.now) {
		t.Errorf("cached entry = %+v, %v; want the refreshed response", e, ok)
	}
}

// sharedPosts sends two concurrent Posts of queryA through one shared
// request, the first, with check first, starting it and the second,
// with check second, joining it, and returns what each was served.
// respond answers the request once both wait on it.
func sharedPosts(t *testing.T, respond http.HandlerFunc, opts Options, first, second func([]byte) error) (*harness, [2]Result, [2]error) {
	t.Helper()
	arrived := make(chan struct{})
	release := make(chan struct{})
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		respond(w, r)
	})
	res, errs := sharePosts(t, hs, opts, arrived, release, first, second)
	return hs, res, errs
}

func sharePosts(t *testing.T, hs *harness, opts Options, arrived, release chan struct{}, first, second func([]byte) error) ([2]Result, [2]error) {
	t.Helper()
	f := hs.fetcher(opts)
	var (
		wg   sync.WaitGroup
		res  [2]Result
		errs [2]error
	)
	wg.Go(func() {
		res[0], errs[0] = f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), first)
	})
	<-arrived
	wg.Go(func() {
		res[1], errs[1] = f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), second)
	})
	key := cache.Key(http.MethodPost, hs.url(), []byte(queryA))
	for postWaiters(f, key) < 2 {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	return res, errs
}

func TestPostCheckVetsSharedResponse(t *testing.T) {
	errBad := errors.New("bad payload")
	reject := func([]byte) error { return errBad }
	hs, res, errs := sharedPosts(t, echoHandler(t), Options{}, nil, reject)

	checkResult(t, res[0], errs[0], OriginLive, queryA)
	if !errors.Is(errs[1], errBad) {
		t.Errorf("err = %v, want the check's error", errs[1])
	}
	if res[1].Body != nil {
		t.Errorf("rejected Post served %q", res[1].Body)
	}
	if n := hs.hits.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestPostSharedRejectionIsTheStartersAlone(t *testing.T) {
	errBad := errors.New("bad payload")
	reject := func([]byte) error { return errBad }
	accept := func([]byte) error { return nil }
	hs, res, errs := sharedPosts(t, echoHandler(t), Options{}, reject, accept)

	if !errors.Is(errs[0], errBad) || res[0].Body != nil {
		t.Errorf("starter = %q, %v; want the check's error", res[0].Body, errs[0])
	}
	// The joining Post's own check accepts the body the starter's
	// rejected, so it is served, live, but never cached.
	checkResult(t, res[1], errs[1], OriginLive, queryA)
	if n := hs.hits.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if _, ok := hs.cache.Get(cache.Key(http.MethodPost, hs.url(), []byte(queryA))); ok {
		t.Error("a body the starter's check rejected was cached")
	}
}

func TestPostSharedFallsBackToItsOwnCachedEntry(t *testing.T) {
	errBad := errors.New("bad payload")
	reject := func([]byte) error { return errBad }
	accept := func([]byte) error { return nil }
	arrived := make(chan struct{})
	release := make(chan struct{})
	var seeded atomic.Bool
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if !seeded.Load() {
			echoHandler(t)(w, r)
			return
		}
		close(arrived)
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	res, err := hs.fetcher(Options{}).Post(t.Context(), hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)
	seeded.Store(true)

	// The starter's check passes over the fresh entry and the upstream
	// fails; the joining Post's check accepts the entry and is served
	// it, as the starter's choice is not its own.
	got, errs := sharePosts(t, hs, Options{Retries: -1}, arrived, release, reject, accept)
	if !errors.Is(errs[0], ErrNoData) {
		t.Errorf("starter err = %v, want ErrNoData", errs[0])
	}
	checkResult(t, got[1], errs[1], OriginCache, queryA)
}

func TestPostSharedTransientFallsBackToItsOwnCachedEntry(t *testing.T) {
	reject := func([]byte) error { return errors.New("bad payload") }
	limitedB := func(b []byte) error {
		if string(b) == queryB {
			return ErrTransient
		}
		return nil
	}
	arrived := make(chan struct{})
	release := make(chan struct{})
	var seeded atomic.Bool
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if !seeded.Load() {
			echoHandler(t)(w, r)
			return
		}
		close(arrived)
		<-release
		write(t, w, queryB)
	})
	res, err := hs.fetcher(Options{}).Post(t.Context(), hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)
	seeded.Store(true)

	// The starter rejects the live body for good; the joining Post's
	// check finds it transient and falls back to the entry it accepts.
	got, errs := sharePosts(t, hs, Options{}, arrived, release, reject, limitedB)
	if errs[0] == nil || errors.Is(errs[0], ErrNoData) {
		t.Errorf("starter err = %v, want the check's error", errs[0])
	}
	checkResult(t, got[1], errs[1], OriginCache, queryA)
}

// TestPostSharedTransientStarterServesStale starts a shared Post whose
// check accepts the expired entry A but finds the live body B rate
// limited, so it is served A stale once its retries run out. A caller
// whose check rejects A but accepts B is served B, uncached, as it
// would be had it started the request.
func TestPostSharedTransientStarterServesStale(t *testing.T) {
	limitedB := func(b []byte) error {
		if string(b) == queryB {
			return fmt.Errorf("%w: rate limited", ErrTransient)
		}
		return nil
	}
	onlyB := func(b []byte) error {
		if string(b) != queryB {
			return errors.New("bad payload")
		}
		return nil
	}
	arrived := make(chan struct{})
	release := make(chan struct{})
	var seeded atomic.Bool
	var once sync.Once
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if !seeded.Load() {
			echoHandler(t)(w, r)
			return
		}
		once.Do(func() {
			close(arrived)
			<-release
		})
		write(t, w, queryB)
	})
	res, err := hs.fetcher(Options{}).Post(t.Context(), hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)
	seeded.Store(true)
	hs.now = hs.now.Add(DefaultMaxAge + time.Hour)

	got, errs := sharePosts(t, hs, Options{}, arrived, release, limitedB, onlyB)
	checkResult(t, got[0], errs[0], OriginStale, queryA)
	checkResult(t, got[1], errs[1], OriginLive, queryB)
	if n := hs.hits.Load(); n != 1+DefaultRetries+1 {
		t.Errorf("requests = %d, want 1 then %d", n, DefaultRetries+1)
	}
	if e, ok := hs.cache.Get(cache.Key(http.MethodPost, hs.url(), []byte(queryA))); !ok || string(e.Body) != queryA {
		t.Errorf("cached entry = %+v, %v; want the first response", e, ok)
	}
}

// postWaiters returns how many callers wait on the in-flight call for
// key.
func postWaiters(f *Fetcher, key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.inflight[key]; ok {
		return c.waiters
	}
	return 0
}
