// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package source fetches upstream release data over HTTP, through the
// on-disk cache, with timeouts, retries, a size cap and conditional
// requests. Each upstream source lives in a subpackage.
package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
)

// UserAgent is the User-Agent header the fetcher sends.
const UserAgent = "supported-versions-action (+https://github.com/lfreleng-actions/supported-versions-action)"

// Defaults for zero Options fields.
const (
	// DefaultMaxAge is how long an entry is served without network.
	DefaultMaxAge = 6 * time.Hour
	// DefaultTimeout bounds each attempt.
	DefaultTimeout = 5 * time.Second
	// DefaultRetries is the number of extra attempts after the first.
	DefaultRetries = 2
	// DefaultMaxBody is the largest response body accepted, in bytes.
	DefaultMaxBody = 8 << 20
)

const (
	backoffBase   = 500 * time.Millisecond
	maxRetryAfter = 30 * time.Second
	maxRedirects  = 10
)

var (
	// ErrNotFound reports an HTTP 404 from upstream. No cached entry
	// is served in its place.
	ErrNotFound = errors.New("not found")
	// ErrNoData reports that neither the network nor the cache could
	// supply a response.
	ErrNoData = errors.New("no data available")
	// errRedirect reports a redirect the fetcher refused to follow.
	errRedirect = errors.New("redirect to a different host refused")
	// errBudget is the cause of a shared fetch's context ending because
	// its time budget ran out, as opposed to every caller leaving.
	errBudget = errors.New("fetch time budget spent")
)

// Origin says where a Result's body came from.
type Origin string

// Result origins.
const (
	// OriginLive is a response fetched or revalidated just now.
	OriginLive Origin = "live"
	// OriginCache is a cached entry younger than MaxAge, served without
	// network.
	OriginCache Origin = "cache"
	// OriginStale is a cached entry older than MaxAge, served because
	// the network failed or the fetcher is Offline.
	OriginStale Origin = "stale"
)

// Result is the outcome of a successful Get.
type Result struct {
	// Body is the response body. Callers must not modify it.
	Body []byte
	// Source is where Body came from.
	Source Origin
	// FetchedAt is when Body was fetched or last revalidated.
	FetchedAt time.Time
	// Warnings describe degraded results, such as a stale entry.
	Warnings []string
}

// Options configure a Fetcher. A zero field takes its default.
type Options struct {
	// Client sends requests; nil uses a new http.Client. The fetcher
	// works on a copy that refuses redirects to a different host.
	Client *http.Client
	// Cache stores responses; nil disables caching.
	Cache *cache.Cache
	// MaxAge is how long an entry is served without network
	// (default DefaultMaxAge).
	MaxAge time.Duration
	// Timeout bounds each attempt (default DefaultTimeout).
	Timeout time.Duration
	// Retries is the number of extra attempts after the first
	// (default DefaultRetries). A negative value disables retries.
	Retries int
	// MaxBody is the largest body accepted, in bytes (default
	// DefaultMaxBody). A larger response is an error.
	MaxBody int64
	// Offline never touches the network and serves cached entries of
	// any age.
	Offline bool
	// Refresh ignores cached entries when reading, so it sends no
	// conditional request and serves no stale entry, but still stores
	// results.
	Refresh bool
	// Now returns the current time (default time.Now). Tests inject it.
	Now func() time.Time
	// Sleep waits between attempts (default: a timer that stops early
	// when ctx ends). Tests inject it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Fetcher fetches URLs through the cache. It is safe for concurrent
// use; concurrent Gets of one URL share a single request.
type Fetcher struct {
	opts   Options
	client *http.Client

	mu       sync.Mutex
	inflight map[string]*call
}

// call is one in-flight fetch that every concurrent Get for its key
// waits on. Its context is detached from every caller's, so that one
// caller's cancellation does not fail the others; cancel stops it once
// no caller waits. waiters is guarded by Fetcher.mu; res and err are
// set before done closes.
type call struct {
	done    chan struct{}
	cancel  context.CancelFunc
	res     Result
	err     error
	waiters int
}

// New returns a Fetcher configured by opts.
func New(opts Options) *Fetcher {
	if opts.MaxAge == 0 {
		opts.MaxAge = DefaultMaxAge
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	switch {
	case opts.Retries == 0:
		opts.Retries = DefaultRetries
	case opts.Retries < 0:
		opts.Retries = 0
	}
	if opts.MaxBody == 0 {
		opts.MaxBody = DefaultMaxBody
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	client := &http.Client{}
	if opts.Client != nil {
		c := *opts.Client
		client = &c
	}
	client.CheckRedirect = sameHostRedirect(client.CheckRedirect)
	return &Fetcher{opts: opts, client: client, inflight: make(map[string]*call)}
}

// sameHostRedirect refuses a redirect that changes the scheme or host,
// then defers to next, or to the default limit when next is nil.
func sameHostRedirect(next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		orig := via[0].URL
		if req.URL.Host != orig.Host || req.URL.Scheme != orig.Scheme {
			return fmt.Errorf("%w: %s to %s", errRedirect, orig.Redacted(), req.URL.Redacted())
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Get returns the body at url, following these steps:
//
//  1. A cached entry younger than MaxAge is served as OriginCache.
//  2. Otherwise the URL is requested, conditionally when an entry
//     exists. A 200 is stored and served as OriginLive; a 304 renews
//     the entry and serves it as OriginLive.
//  3. Transport errors, 429 and 5xx are retried with exponential
//     backoff and full jitter, honoring a Retry-After of up to 30
//     seconds. Other 4xx are not retried.
//  4. A 404 returns an error wrapping ErrNotFound; any other 4xx, or
//     a body over MaxBody, returns an error. MaxBody applies to cached
//     bodies too: one over it is an error wherever it would be served.
//  5. When retries run out, or the time budget below does, a cached
//     entry of any age is served as OriginStale with a warning; with
//     none, the error wraps ErrNoData.
//
// When Offline, only step 1 applies, extended to entries of any age,
// which are served as OriginStale once older than MaxAge.
//
// Concurrent Gets of one URL share one fetch, which no single caller's
// cancellation or deadline stops: a caller whose ctx ends returns
// ctx.Err() at once, and the fetch is canceled only when every caller
// has gone. The fetch itself is bounded by Timeout and the retry
// budget; running out of that budget counts as running out of retries.
func (f *Fetcher) Get(ctx context.Context, url string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	key := cache.Key(http.MethodGet, url, nil)
	f.mu.Lock()
	c, ok := f.inflight[key]
	if !ok {
		c = f.start(ctx, key, url)
	}
	c.waiters++
	f.mu.Unlock()
	select {
	case <-c.done:
		return c.res.clone(), c.err
	case <-ctx.Done():
		f.leave(key, c)
		return Result{}, ctx.Err()
	}
}

// start launches the shared fetch for key and registers it as in
// flight. The caller holds f.mu. The fetch keeps ctx's values but not
// its cancellation or deadline, and runs for at most budget.
func (f *Fetcher) start(ctx context.Context, key, url string) *call {
	sctx, cancel := context.WithTimeoutCause(context.WithoutCancel(ctx), f.budget(), errBudget)
	c := &call{done: make(chan struct{}), cancel: cancel}
	f.inflight[key] = c
	go func() {
		defer cancel()
		res, err := f.get(sctx, key, url)
		f.mu.Lock()
		if f.inflight[key] == c {
			delete(f.inflight, key)
		}
		f.mu.Unlock()
		c.res, c.err = res, err
		close(c.done)
	}()
	return c
}

// leave withdraws a caller whose context ended from c, canceling the
// shared fetch when no caller still waits on it. A later Get for the
// key then starts afresh rather than joining a canceled fetch.
func (f *Fetcher) leave(key string, c *call) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.waiters--
	if c.waiters > 0 {
		return
	}
	c.cancel()
	if f.inflight[key] == c {
		delete(f.inflight, key)
	}
}

// budget bounds a shared fetch: every attempt's Timeout plus the
// longest wait before each retry.
func (f *Fetcher) budget() time.Duration {
	d := time.Duration(f.opts.Retries+1) * f.opts.Timeout
	for attempt := 1; attempt <= f.opts.Retries; attempt++ {
		d += max(maxRetryAfter, backoffBase<<min(attempt-1, 16))
	}
	return d
}

func (r Result) clone() Result {
	r.Body = bytes.Clone(r.Body)
	r.Warnings = append([]string(nil), r.Warnings...)
	return r
}

func (f *Fetcher) get(ctx context.Context, key, url string) (Result, error) {
	var (
		entry cache.Entry
		have  bool
	)
	if f.opts.Cache != nil && !f.opts.Refresh {
		entry, have = f.opts.Cache.Get(key)
		have = have && entry.Method == http.MethodGet && entry.URL == url
	}
	now := f.opts.Now()
	age := now.Sub(entry.FetchedAt)
	fresh := have && age >= 0 && age < f.opts.MaxAge
	switch {
	case fresh:
		if err := f.fits(entry); err != nil {
			return Result{}, err
		}
		return Result{Body: entry.Body, Source: OriginCache, FetchedAt: entry.FetchedAt}, nil
	case f.opts.Offline && have:
		if err := f.fits(entry); err != nil {
			return Result{}, err
		}
		return f.stale(url, entry, age, errors.New("offline")), nil
	case f.opts.Offline:
		return Result{}, fmt.Errorf("%s: %w: offline with no cached entry", url, ErrNoData)
	}

	var cond *cache.Entry
	if have {
		cond = &entry
	}
	var lastErr error
	for attempt := 0; attempt <= f.opts.Retries; attempt++ {
		if attempt > 0 {
			if err := f.opts.Sleep(ctx, f.delay(attempt, lastErr)); err != nil {
				if !spent(ctx) {
					return Result{}, err
				}
				lastErr = fmt.Errorf("%w; then %w", lastErr, errBudget)
				break
			}
		}
		res, err := f.attempt(ctx, key, url, cond)
		if err == nil {
			return res, nil
		}
		var re *retryable
		if !errors.As(err, &re) {
			return Result{}, err
		}
		lastErr = err
		if ctx.Err() != nil {
			if !spent(ctx) {
				return Result{}, ctx.Err()
			}
			lastErr = fmt.Errorf("%w; %w", err, errBudget)
			break
		}
	}
	if have {
		if err := f.fits(entry); err != nil {
			return Result{}, fmt.Errorf("%w; after %w", err, lastErr)
		}
		return f.stale(url, entry, f.opts.Now().Sub(entry.FetchedAt), lastErr), nil
	}
	return Result{}, fmt.Errorf("%s: %w: %w", url, ErrNoData, lastErr)
}

// spent reports whether ctx, a shared fetch's context, ended because
// its time budget ran out rather than because every caller left.
func spent(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errBudget)
}

// fits returns an error when a cached entry's body exceeds MaxBody, so
// that no path serves a body larger than a live response may be.
func (f *Fetcher) fits(e cache.Entry) error {
	if n := int64(len(e.Body)); n > f.opts.MaxBody {
		return fmt.Errorf("%s: cached body of %d bytes exceeds the %d byte limit", e.URL, n, f.opts.MaxBody)
	}
	return nil
}

func (f *Fetcher) stale(url string, e cache.Entry, age time.Duration, cause error) Result {
	return Result{
		Body:      e.Body,
		Source:    OriginStale,
		FetchedAt: e.FetchedAt,
		Warnings: []string{fmt.Sprintf("%s: serving a cached response %s old: %v",
			url, age.Round(time.Second), cause)},
	}
}

// retryable is an attempt failure worth retrying. retryAfter is the
// upstream's Retry-After, or zero.
type retryable struct {
	err        error
	retryAfter time.Duration
}

func (r *retryable) Error() string { return r.err.Error() }
func (r *retryable) Unwrap() error { return r.err }

// delay returns how long to wait before attempt (1 for the first
// retry): the upstream's Retry-After when it is within maxRetryAfter,
// otherwise a full-jitter backoff below backoffBase*2^(attempt-1).
func (f *Fetcher) delay(attempt int, lastErr error) time.Duration {
	var re *retryable
	if errors.As(lastErr, &re) && re.retryAfter > 0 && re.retryAfter <= maxRetryAfter {
		return re.retryAfter
	}
	ceiling := backoffBase << min(attempt-1, 16)
	return rand.N(ceiling) //nolint:gosec // G404: jitter needs no cryptographic randomness.
}

// attempt makes one request. Errors worth retrying are *retryable.
func (f *Fetcher) attempt(ctx context.Context, key, url string, cond *cache.Entry) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, f.opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", url, err)
	}
	req.Header.Set("User-Agent", UserAgent)
	if cond != nil {
		if cond.ETag != "" {
			req.Header.Set("If-None-Match", cond.ETag)
		}
		if cond.LastModified != "" {
			req.Header.Set("If-Modified-Since", cond.LastModified)
		}
	}
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, errRedirect) {
			return Result{}, err
		}
		return Result{}, &retryable{err: err}
	}
	defer resp.Body.Close() //nolint:errcheck // The body is fully read or abandoned.

	switch {
	case resp.StatusCode == http.StatusOK:
		body, err := f.read(resp)
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", url, err)
		}
		e := cache.Entry{
			Method:       http.MethodGet,
			URL:          url,
			Status:       resp.StatusCode,
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
			FetchedAt:    f.opts.Now(),
			Body:         body,
		}
		return f.store(key, e), nil
	case resp.StatusCode == http.StatusNotModified && cond != nil:
		if err := f.fits(*cond); err != nil {
			return Result{}, err
		}
		e := *cond
		e.FetchedAt = f.opts.Now()
		return f.store(key, e), nil
	case resp.StatusCode == http.StatusNotFound:
		return Result{}, fmt.Errorf("%s: %w", url, ErrNotFound)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return Result{}, &retryable{
			err:        fmt.Errorf("%s: HTTP %s", url, resp.Status),
			retryAfter: retryAfter(resp.Header.Get("Retry-After"), f.opts.Now()),
		}
	default:
		return Result{}, fmt.Errorf("%s: unexpected HTTP %s", url, resp.Status)
	}
}

// read returns the response body, or an error when it exceeds MaxBody.
// A transport failure while reading is *retryable.
func (f *Fetcher) read(resp *http.Response) ([]byte, error) {
	if resp.ContentLength > f.opts.MaxBody {
		return nil, fmt.Errorf("body of %d bytes exceeds the %d byte limit", resp.ContentLength, f.opts.MaxBody)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.opts.MaxBody+1))
	if err != nil {
		return nil, &retryable{err: err}
	}
	if int64(len(body)) > f.opts.MaxBody {
		return nil, fmt.Errorf("body exceeds the %d byte limit", f.opts.MaxBody)
	}
	return body, nil
}

// store saves e when a cache is configured and returns it as a live
// Result. A failure to store becomes a warning, not an error.
func (f *Fetcher) store(key string, e cache.Entry) Result {
	res := Result{Body: e.Body, Source: OriginLive, FetchedAt: e.FetchedAt}
	if f.opts.Cache != nil {
		if err := f.opts.Cache.Put(key, e); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: not cached: %v", e.URL, err))
		}
	}
	return res
}

// retryAfter parses a Retry-After header, given in seconds or as an
// HTTP date, and returns zero when it is absent or malformed.
func retryAfter(h string, now time.Time) time.Duration {
	if h == "" {
		return 0
	}
	if s, err := strconv.Atoi(h); err == nil {
		if s < 0 {
			return 0
		}
		return time.Duration(min(s, int(maxRetryAfter/time.Second)+1)) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(t.Sub(now), 0)
	}
	return 0
}
