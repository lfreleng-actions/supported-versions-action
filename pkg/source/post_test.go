// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// limitedB rejects queryB as rate limited, and accepts any other body.
func limitedB(b []byte) error {
	if string(b) == queryB {
		return fmt.Errorf("%w: rate limited", ErrTransient)
	}
	return nil
}

// gated answers with respond, holding the first want requests until
// all of them have arrived, so that the Posts sending them overlap.
// Later requests pass straight through.
func gated(t *testing.T, want int32, seeded *atomic.Bool, respond http.HandlerFunc) http.HandlerFunc {
	var arrived atomic.Int32
	release := make(chan struct{})
	return func(w http.ResponseWriter, r *http.Request) {
		if !seeded.Load() {
			echoHandler(t)(w, r)
			return
		}
		if n := arrived.Add(1); n <= want {
			if n == want {
				close(release)
			}
			select {
			case <-release:
			case <-time.After(10 * time.Second):
				t.Error("the concurrent Posts did not each send a request")
			}
		}
		respond(w, r)
	}
}

// concurrentPosts seeds entry queryA unless age is negative, aging it
// by age, then sends one Post of queryA per check, all at once, and
// returns what each was served. respond answers every request after
// the seed, and the first want of them are held until all arrive.
func concurrentPosts(t *testing.T, age time.Duration, want int32, respond http.HandlerFunc, checks ...func([]byte) error) (*harness, []Result, []error) {
	t.Helper()
	var seeded atomic.Bool
	hs := newHarness(t, gated(t, want, &seeded, respond))
	f := hs.fetcher(Options{})
	if age >= 0 {
		res, err := f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), nil)
		checkResult(t, res, err, OriginLive, queryA)
		hs.now = hs.now.Add(age)
	}
	seeded.Store(true)

	res := make([]Result, len(checks))
	errs := make([]error, len(checks))
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Go(func() {
			res[i], errs[i] = f.Post(t.Context(), hs.url(), []byte(queryA), authHeader(), check)
		})
	}
	wg.Wait()
	return hs, res, errs
}

// TestPostConcurrentCallersAreIndependent sends concurrent Posts of one
// body with different checks: each sends its own request and gets what
// it would get alone, never a result another caller's check decided.
func TestPostConcurrentCallersAreIndependent(t *testing.T) {
	errBad := errors.New("bad payload")
	reject := func([]byte) error { return errBad }
	accept := func([]byte) error { return nil }
	notA := func(b []byte) error {
		if string(b) == queryA {
			return errBad
		}
		return nil
	}
	answerB := func(w http.ResponseWriter, _ *http.Request) { write(t, w, queryB) }
	expired := DefaultMaxAge + time.Hour

	t.Run("one body, two verdicts", func(t *testing.T) {
		hs, res, errs := concurrentPosts(t, -1, 2, echoHandler(t), reject, accept)
		if !errors.Is(errs[0], errBad) || res[0].Body != nil {
			t.Errorf("rejecting Post = %q, %v; want its check's error", res[0].Body, errs[0])
		}
		checkResult(t, res[1], errs[1], OriginLive, queryA)
		if n := hs.hits.Load(); n != 2 {
			t.Errorf("requests = %d, want 2", n)
		}
	})
	t.Run("fresh entry only one caller accepts", func(t *testing.T) {
		notFound := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }
		_, res, errs := concurrentPosts(t, 0, 1, notFound, notA, accept)
		if !errors.Is(errs[0], ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", errs[0])
		}
		checkResult(t, res[1], errs[1], OriginCache, queryA)
	})
	t.Run("expired entry, live body one caller finds rate limited", func(t *testing.T) {
		_, res, errs := concurrentPosts(t, expired, 2, answerB, accept, limitedB)
		checkResult(t, res[0], errs[0], OriginLive, queryB)
		checkResult(t, res[1], errs[1], OriginStale, queryA)
	})
	t.Run("expired entry only one caller accepts", func(t *testing.T) {
		_, res, errs := concurrentPosts(t, expired, 2, answerB, limitedB, notA)
		checkResult(t, res[0], errs[0], OriginStale, queryA)
		checkResult(t, res[1], errs[1], OriginLive, queryB)
	})
}

func TestPostReturnsWhenItsContextEnds(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		echoHandler(t)(w, r)
	})
	defer close(release)
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := hs.fetcher(Options{}).Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
		errc <- err
	}()
	<-arrived
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestPostUnderSpentBudgetFallsBack(t *testing.T) {
	hs := newHarness(t, echoHandler(t))
	ctx := t.Context()
	res, err := hs.fetcher(Options{}).Post(ctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginLive, queryA)

	// A budget of one millisecond, spent before any Post under it.
	f := hs.fetcher(Options{Timeout: time.Millisecond, Retries: -1})
	bctx, cancel := f.Budget(ctx)
	defer cancel()
	<-bctx.Done()

	res, err = f.Post(bctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginCache, queryA)
	hs.now = hs.now.Add(DefaultMaxAge + time.Hour)
	res, err = f.Post(bctx, hs.url(), []byte(queryA), authHeader(), nil)
	checkResult(t, res, err, OriginStale, queryA)
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], errBudget.Error()) {
		t.Errorf("warnings = %q, want one naming the spent budget", res.Warnings)
	}
	if _, err = f.Post(bctx, hs.url(), []byte(queryB), authHeader(), nil); !errors.Is(err, ErrNoData) {
		t.Errorf("err = %v, want ErrNoData", err)
	}
	if n := hs.hits.Load(); n != 1 {
		t.Errorf("requests = %d, want only the first", n)
	}
}
