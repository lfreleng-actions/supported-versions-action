// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	etagV1   = `"v1"`
	bodyV1   = `{"v":1}`
	bodyV2   = `{"v":2}`
	pathData = "/data"
)

// harness is a fetcher wired to a test server, a cache, a fixed clock
// and a sleep that records instead of waiting.
type harness struct {
	t      *testing.T
	srv    *httptest.Server
	cache  *cache.Cache
	now    time.Time
	sleeps []time.Duration
	hits   atomic.Int32
}

func newHarness(t *testing.T, h http.HandlerFunc) *harness {
	t.Helper()
	hs := &harness{t: t, now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	hs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hs.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(hs.srv.Close)
	c, err := cache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	hs.cache = c
	return hs
}

func (hs *harness) fetcher(opts Options) *Fetcher {
	opts.Cache = hs.cache
	opts.Now = func() time.Time { return hs.now }
	opts.Sleep = func(_ context.Context, d time.Duration) error {
		hs.sleeps = append(hs.sleeps, d)
		return nil
	}
	return New(opts)
}

func (hs *harness) url() string { return hs.srv.URL + pathData }

// seed stores an entry for the harness URL fetched age ago.
func (hs *harness) seed(age time.Duration, body string) {
	hs.t.Helper()
	err := hs.cache.Put(cache.Key(http.MethodGet, hs.url(), nil), cache.Entry{
		Method:    http.MethodGet,
		URL:       hs.url(),
		Status:    http.StatusOK,
		ETag:      etagV1,
		FetchedAt: hs.now.Add(-age),
		Body:      []byte(body),
	})
	if err != nil {
		hs.t.Fatal(err)
	}
}

func (hs *harness) entry() cache.Entry {
	hs.t.Helper()
	e, ok := hs.cache.Get(cache.Key(http.MethodGet, hs.url(), nil))
	if !ok {
		hs.t.Fatal("no cache entry")
	}
	return e
}

func failHandler(t *testing.T) http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {
		t.Error("unexpected request")
	}
}

func statusHandler(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}
}

// sequence answers the nth request with the nth handler, repeating
// the last one.
func sequence(hs ...http.HandlerFunc) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		i := min(int(n.Add(1))-1, len(hs)-1)
		hs[i](w, r)
	}
}

func okHandler(t *testing.T, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", etagV1)
		write(t, w, body)
	}
}

func write(t *testing.T, w http.ResponseWriter, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		t.Errorf("writing the response: %v", err)
	}
}

func checkResult(t *testing.T, res Result, err error, src Origin, body string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if res.Source != src {
		t.Errorf("Source = %q, want %q", res.Source, src)
	}
	if string(res.Body) != body {
		t.Errorf("Body = %q, want %q", res.Body, body)
	}
}

func TestFreshHitMakesNoRequest(t *testing.T) {
	hs := newHarness(t, failHandler(t))
	hs.seed(time.Hour, bodyV1)
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginCache, bodyV1)
	if hs.hits.Load() != 0 {
		t.Errorf("requests = %d, want 0", hs.hits.Load())
	}
}

func TestStaleEntryRevalidates(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != etagV1 {
			t.Errorf("If-None-Match = %q, want %q", got, etagV1)
		}
		w.WriteHeader(http.StatusNotModified)
	})
	hs.seed(7*time.Hour, bodyV1)
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV1)
	if e := hs.entry(); !e.FetchedAt.Equal(hs.now) {
		t.Errorf("entry FetchedAt = %v, want renewed to %v", e.FetchedAt, hs.now)
	}
}

func TestOKStores(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != UserAgent {
			t.Errorf("User-Agent = %q, want %q", got, UserAgent)
		}
		if got := r.Header.Get("If-None-Match"); got != "" {
			t.Errorf("unexpected If-None-Match %q", got)
		}
		okHandler(t, bodyV2)(w, r)
	})
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)
	e := hs.entry()
	if string(e.Body) != bodyV2 || e.ETag != etagV1 || !e.FetchedAt.Equal(hs.now) || e.Status != http.StatusOK {
		t.Errorf("stored entry = %+v", e)
	}
}

func TestRetryOnServerError(t *testing.T) {
	hs := newHarness(t, sequence(statusHandler(http.StatusInternalServerError), okHandler(t, bodyV2)))
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)
	if hs.hits.Load() != 2 {
		t.Errorf("requests = %d, want 2", hs.hits.Load())
	}
	if len(hs.sleeps) != 1 || hs.sleeps[0] < 0 || hs.sleeps[0] >= backoffBase {
		t.Errorf("sleeps = %v, want one in [0, %v)", hs.sleeps, backoffBase)
	}
}

func TestRetryOnTooManyRequestsHonorsRetryAfter(t *testing.T) {
	hs := newHarness(t, sequence(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}, okHandler(t, bodyV2)))
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)
	if len(hs.sleeps) != 1 || hs.sleeps[0] != 7*time.Second {
		t.Errorf("sleeps = %v, want [7s]", hs.sleeps)
	}
}

func TestBackoffGrows(t *testing.T) {
	hs := newHarness(t, statusHandler(http.StatusServiceUnavailable))
	_, err := hs.fetcher(Options{Retries: 4}).Get(t.Context(), hs.url())
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
	if len(hs.sleeps) != 4 {
		t.Fatalf("sleeps = %v, want 4", hs.sleeps)
	}
	for i, d := range hs.sleeps {
		if ceiling := backoffBase << i; d < 0 || d >= ceiling {
			t.Errorf("sleep %d = %v, want in [0, %v)", i, d, ceiling)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	f := New(Options{})
	tests := []struct {
		header  string
		honor   bool
		seconds int
	}{
		{"30", true, 30},
		{now.Add(10 * time.Second).Format(http.TimeFormat), true, 10},
		{"31", false, 0},
		{"3600", false, 0},
		{"soon", false, 0},
		{"-1", false, 0},
		{"", false, 0},
	}
	for _, tt := range tests {
		last := &retryable{err: errors.New("x"), retryAfter: retryAfter(tt.header, now)}
		got := f.delay(1, last)
		if tt.honor && got != time.Duration(tt.seconds)*time.Second {
			t.Errorf("Retry-After %q: delay = %v, want %ds", tt.header, got, tt.seconds)
		}
		if !tt.honor && got >= backoffBase {
			t.Errorf("Retry-After %q: delay = %v, want backoff below %v", tt.header, got, backoffBase)
		}
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	hs := newHarness(t, statusHandler(http.StatusBadRequest))
	hs.seed(7*time.Hour, bodyV1)
	_, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrNoData) {
		t.Errorf("err = %v, want a plain error", err)
	}
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
}

func TestNotFound(t *testing.T) {
	hs := newHarness(t, statusHandler(http.StatusNotFound))
	hs.seed(7*time.Hour, bodyV1)
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if res.Body != nil {
		t.Errorf("served %q on a 404", res.Body)
	}
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
}

func TestExhaustedRetriesServeStale(t *testing.T) {
	hs := newHarness(t, statusHandler(http.StatusServiceUnavailable))
	hs.seed(7*time.Hour, bodyV1)
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginStale, bodyV1)
	if hs.hits.Load() != 3 {
		t.Errorf("requests = %d, want 3", hs.hits.Load())
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], hs.url()) ||
		!strings.Contains(res.Warnings[0], "7h0m0s") {
		t.Errorf("Warnings = %q, want one naming the URL and age", res.Warnings)
	}
}

func TestNoEntryIsNoData(t *testing.T) {
	hs := newHarness(t, statusHandler(http.StatusBadGateway))
	_, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	if !errors.Is(err, ErrNoData) {
		t.Errorf("err = %v, want ErrNoData", err)
	}
}

func TestTransportErrorRetried(t *testing.T) {
	hs := newHarness(t, failHandler(t))
	url := hs.url()
	hs.seed(7*time.Hour, bodyV1)
	hs.srv.Close()
	res, err := hs.fetcher(Options{}).Get(t.Context(), url)
	checkResult(t, res, err, OriginStale, bodyV1)
	if len(hs.sleeps) != DefaultRetries {
		t.Errorf("sleeps = %v, want %d", hs.sleeps, DefaultRetries)
	}
}

func TestOffline(t *testing.T) {
	hs := newHarness(t, failHandler(t))
	f := hs.fetcher(Options{Offline: true})

	_, err := f.Get(t.Context(), hs.url())
	if !errors.Is(err, ErrNoData) {
		t.Errorf("no entry: err = %v, want ErrNoData", err)
	}

	hs.seed(time.Hour, bodyV1)
	res, err := f.Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginCache, bodyV1)

	hs.seed(30*24*time.Hour, bodyV2)
	res, err = f.Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginStale, bodyV2)
	if len(res.Warnings) != 1 {
		t.Errorf("Warnings = %q, want one", res.Warnings)
	}
	if hs.hits.Load() != 0 {
		t.Errorf("requests = %d, want 0", hs.hits.Load())
	}
}

func TestRefresh(t *testing.T) {
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != "" {
			t.Errorf("unexpected If-None-Match %q", got)
		}
		okHandler(t, bodyV2)(w, r)
	})
	hs.seed(time.Hour, bodyV1)
	res, err := hs.fetcher(Options{Refresh: true}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)
	if e := hs.entry(); string(e.Body) != bodyV2 {
		t.Errorf("stored body = %q, want %q", e.Body, bodyV2)
	}
}

func TestMaxBody(t *testing.T) {
	big := strings.Repeat("x", 11)
	for name, h := range map[string]http.HandlerFunc{
		"with length": okHandler(t, big),
		"chunked": func(w http.ResponseWriter, _ *http.Request) {
			for _, c := range big {
				write(t, w, string(c))
				w.(http.Flusher).Flush()
			}
		},
	} {
		hs := newHarness(t, h)
		hs.seed(7*time.Hour, bodyV1)
		_, err := hs.fetcher(Options{MaxBody: 10}).Get(t.Context(), hs.url())
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("%s: err = %v, want a size error", name, err)
		}
		if hs.hits.Load() != 1 {
			t.Errorf("%s: requests = %d, want 1", name, hs.hits.Load())
		}
	}
}

func TestMaxBodyCached(t *testing.T) {
	big := strings.Repeat("x", 11)
	notModified := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != etagV1 {
			t.Errorf("If-None-Match = %q, want %q", r.Header.Get("If-None-Match"), etagV1)
		}
		w.WriteHeader(http.StatusNotModified)
	}
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		age      time.Duration
		offline  bool
		requests int32
	}{
		{"fresh", failHandler(t), time.Hour, false, 0},
		{"offline", failHandler(t), 7 * time.Hour, true, 0},
		{"after 304", notModified, 7 * time.Hour, false, 1},
		{"stale", statusHandler(http.StatusServiceUnavailable), 7 * time.Hour, false, 3},
	}
	for _, tt := range tests {
		hs := newHarness(t, tt.handler)
		hs.seed(tt.age, big)
		res, err := hs.fetcher(Options{MaxBody: 10, Offline: tt.offline}).Get(t.Context(), hs.url())
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("%s: err = %v, want a size error", tt.name, err)
		}
		if res.Body != nil {
			t.Errorf("%s: served an oversized cached body", tt.name)
		}
		if hs.hits.Load() != tt.requests {
			t.Errorf("%s: requests = %d, want %d", tt.name, hs.hits.Load(), tt.requests)
		}
	}
}

func TestMaxBodyCachedReplacedByLive(t *testing.T) {
	hs := newHarness(t, okHandler(t, bodyV2))
	hs.seed(7*time.Hour, strings.Repeat("x", 11))
	res, err := hs.fetcher(Options{MaxBody: 10}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)
}

func TestRedirects(t *testing.T) {
	other := httptest.NewServer(failHandler(t))
	t.Cleanup(other.Close)
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pathData:
			http.Redirect(w, r, "/moved", http.StatusFound)
		case "/moved":
			okHandler(t, bodyV2)(w, r)
		default:
			http.Redirect(w, r, other.URL+pathData, http.StatusFound)
		}
	})
	res, err := hs.fetcher(Options{}).Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)

	_, err = hs.fetcher(Options{}).Get(t.Context(), hs.srv.URL+"/away")
	if !errors.Is(err, errRedirect) {
		t.Errorf("err = %v, want a refused redirect", err)
	}
	if len(hs.sleeps) != 0 {
		t.Errorf("refused redirect retried: sleeps = %v", hs.sleeps)
	}
}

func TestConcurrentGetsShareOneRequest(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		okHandler(t, bodyV2)(w, r)
	})
	f := hs.fetcher(Options{})
	const callers = 8
	results := make([]Result, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.Get(t.Context(), hs.url())
		}()
	}
	start(0)
	<-arrived
	for i := 1; i < callers; i++ {
		start(i)
	}
	for waiters(f, hs.url()) < callers {
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	for i := range callers {
		checkResult(t, results[i], errs[i], OriginLive, bodyV2)
	}
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
}

func TestCancelledCallerLeavesOthers(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	hs := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		okHandler(t, bodyV2)(w, r)
	})
	f := hs.fetcher(Options{})
	ctxA, cancelA := context.WithCancel(t.Context())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(t.Context())
	defer cancelB()
	errA := make(chan error, 1)
	go func() {
		_, err := f.Get(ctxA, hs.url())
		errA <- err
	}()
	<-arrived
	type outcome struct {
		res Result
		err error
	}
	outB := make(chan outcome, 1)
	go func() {
		res, err := f.Get(ctxB, hs.url())
		outB <- outcome{res, err}
	}()
	for waiters(f, hs.url()) < 2 {
		runtime.Gosched()
	}
	cancelA()
	if err := <-errA; !errors.Is(err, context.Canceled) {
		t.Errorf("canceled caller: err = %v, want context.Canceled", err)
	}
	close(release)
	got := <-outB
	checkResult(t, got.res, got.err, OriginLive, bodyV2)
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
}

func TestLastCallerLeavingCancelsFetch(t *testing.T) {
	arrived := make(chan struct{})
	aborted := make(chan struct{})
	hs := newHarness(t, sequence(
		func(_ http.ResponseWriter, r *http.Request) {
			close(arrived)
			<-r.Context().Done()
			close(aborted)
		},
		okHandler(t, bodyV2),
	))
	f := hs.fetcher(Options{Retries: -1})
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := f.Get(ctx, hs.url())
		errc <- err
	}()
	<-arrived
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("shared fetch outlived its last caller")
	}
	// A later Get starts afresh instead of joining the canceled fetch.
	res, err := f.Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)
}

func TestCancelledContextMakesNoRequest(t *testing.T) {
	hs := newHarness(t, failHandler(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := hs.fetcher(Options{}).Get(ctx, hs.url()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestBudget(t *testing.T) {
	f := New(Options{Timeout: time.Second, Retries: 2})
	want := 3*time.Second + 2*maxRetryAfter
	if got := f.budget(); got != want {
		t.Errorf("budget = %v, want %v", got, want)
	}
}

// hangHandler answers nothing until the client gives up.
func hangHandler(_ http.ResponseWriter, r *http.Request) {
	<-r.Context().Done()
}

func checkExhausted(t *testing.T, res Result, err error, cached bool) {
	t.Helper()
	if cached {
		checkResult(t, res, err, OriginStale, bodyV1)
		if len(res.Warnings) != 1 {
			t.Errorf("Warnings = %q, want one", res.Warnings)
		}
		return
	}
	if !errors.Is(err, ErrNoData) {
		t.Errorf("err = %v, want ErrNoData", err)
	}
}

func TestTimeoutWithoutRetries(t *testing.T) {
	// With no retries the shared budget equals the one attempt's
	// Timeout, so it can expire first; that is still exhaustion.
	for _, cached := range []bool{true, false} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			hs := newHarness(t, hangHandler)
			if cached {
				hs.seed(7*time.Hour, bodyV1)
			}
			f := hs.fetcher(Options{Retries: -1, Timeout: 50 * time.Millisecond})
			res, err := f.Get(t.Context(), hs.url())
			checkExhausted(t, res, err, cached)
		})
	}
}

// TestSharedContextEnding ends a shared fetch's context during the
// retry wait or a request, either because its budget ran out, which
// counts as exhaustion, or because every caller left, which does not.
func TestSharedContextEnding(t *testing.T) {
	for _, stage := range []string{"wait", "request"} {
		for _, cause := range []error{errBudget, context.Canceled} {
			for _, cached := range []bool{true, false} {
				name := fmt.Sprintf("%s/%v/cached=%t", stage, cause, cached)
				t.Run(name, func(t *testing.T) {
					testSharedContextEnding(t, stage, cause, cached)
				})
			}
		}
	}
}

func testSharedContextEnding(t *testing.T, stage string, cause error, cached bool) {
	ctx, end := context.WithCancelCause(t.Context())
	defer end(nil)
	h := statusHandler(http.StatusServiceUnavailable)
	if stage == "request" {
		h = func(w http.ResponseWriter, r *http.Request) {
			end(cause)
			hangHandler(w, r)
		}
	}
	hs := newHarness(t, h)
	if cached {
		hs.seed(7*time.Hour, bodyV1)
	}
	f := hs.fetcher(Options{})
	if stage == "wait" {
		f.opts.Sleep = func(ctx context.Context, _ time.Duration) error {
			end(cause)
			return ctx.Err()
		}
	}
	res, err := f.get(ctx, cache.Key(http.MethodGet, hs.url(), nil), hs.url())
	if hs.hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hs.hits.Load())
	}
	if cause == errBudget {
		checkExhausted(t, res, err, cached)
		return
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func waiters(f *Fetcher, url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.inflight[cache.Key(http.MethodGet, url, nil)]
	if !ok {
		return 0
	}
	return c.waiters
}

func TestNoCache(t *testing.T) {
	hs := newHarness(t, okHandler(t, bodyV2))
	f := New(Options{Sleep: func(context.Context, time.Duration) error { return nil }})
	res, err := f.Get(t.Context(), hs.url())
	checkResult(t, res, err, OriginLive, bodyV2)
}
