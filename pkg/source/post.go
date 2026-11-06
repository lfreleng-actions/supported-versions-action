// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
)

// Post sends body to url with header and returns the response through
// the cache, following the steps Get does, except that an expired
// entry is not revalidated: the Post is sent without conditional
// headers, which a server holding a matching ETag would answer with
// 412, and the entry is served stale only when retries run out. The
// cache key covers the method, the URL and the body, never header, so
// credentials stay out of both the key and the entry; concurrent Posts
// of one body to one URL share a single request. check, when not nil,
// vets the body of a 200 response before it is stored or served: a
// body it rejects is not cached and fails the Post with check's error,
// without a retry unless that error wraps ErrTransient, which is
// retried and then served a cached entry, or fails with ErrNoData, as
// a 429 is. It also vets every other body this Post would serve: a
// cached entry it rejects is ignored, as if absent. A Post sharing
// another's request is judged by its own check alone, whichever caller
// started the request: it fails when its check rejects the body the
// request returns, with an error wrapping ErrNoData when the rejection
// is transient, and is served a body the starter's check rejected,
// uncached, or a cached entry the starter's check passed over, when
// its own check accepts it.
func (f *Fetcher) Post(ctx context.Context, url string, body []byte, header http.Header, check func([]byte) error) (Result, error) {
	return f.do(ctx, request{method: http.MethodPost, url: url, body: body, header: header, check: check}, 0)
}

// answer returns what the finished shared call c gives the caller of
// req. The call ran the check of whichever caller started it, which
// need not be this caller's, so this caller's check decides alone: it
// judges afresh a live body the starter's check rejected, even one the
// call set aside for a cached entry, vets a body the call served, and,
// when the call or this caller's check finds no data, may accept a
// cached entry the starter's check passed over.
func (f *Fetcher) answer(key string, req request, c *call, maxAge time.Duration) (Result, error) {
	var rj *rejected
	var err error
	switch {
	case errors.As(c.err, &rj):
		if err = judge(req, rj.body); err == nil {
			return rj.result(), nil
		}
	case c.err != nil:
		err = c.err
	default:
		if c.live != nil && judge(req, c.live.body) == nil {
			return c.live.result(), nil
		}
		if err = judge(req, c.res.Body); err == nil {
			return c.res.clone(), nil
		}
	}
	if errors.Is(err, ErrNoData) {
		if entry, ok := f.cached(key, req); ok && f.fits(entry) == nil {
			return f.fallback(req.url, entry, max(maxAge, f.opts.MaxAge), err), nil
		}
	}
	return Result{}, err
}

// judge runs req's check, if any, on body, a response the call shared
// with req did not judge by req's check. A transient rejection wraps
// ErrNoData, as the call's own would after its retries.
func judge(req request, body []byte) error {
	if req.check == nil {
		return nil
	}
	err := req.check(body)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrTransient):
		return fmt.Errorf("%s: %w: %w", req.url, ErrNoData, err)
	default:
		return fmt.Errorf("%s: %w", req.url, err)
	}
}

// fallback serves a cached entry after the network gave no answer: as
// OriginCache while younger than maxAge, otherwise as OriginStale with
// a warning naming cause.
func (f *Fetcher) fallback(url string, e cache.Entry, maxAge time.Duration, cause error) Result {
	age := f.opts.Now().Sub(e.FetchedAt)
	if age >= 0 && age < maxAge {
		return Result{Body: bytes.Clone(e.Body), Source: OriginCache, FetchedAt: e.FetchedAt}
	}
	return f.stale(url, e, age, cause).clone()
}

// rejected is a 200 response the check of the request that fetched it
// refused. It is never cached, and it fails that request, but another
// caller sharing the request judges body with its own check. A
// transient rejection travels inside the *retryable that retries it,
// so the last one is still judged when retries run out, even when the
// request is served a cached entry instead.
type rejected struct {
	err       error
	body      []byte
	fetchedAt time.Time
}

func (r *rejected) Error() string { return r.err.Error() }
func (r *rejected) Unwrap() error { return r.err }

// result serves the rejected body, uncached, to a caller whose own
// check accepts it.
func (r *rejected) result() Result {
	return Result{Body: bytes.Clone(r.body), Source: OriginLive, FetchedAt: r.fetchedAt}
}

// cached returns the cache entry that answers req, if any. An entry
// stored before a check existed, or by a caller with another, is no
// answer when req's check rejects it: the fetch goes upstream rather
// than serve or revalidate it.
func (f *Fetcher) cached(key string, req request) (cache.Entry, bool) {
	if f.opts.Cache == nil || f.opts.Refresh {
		return cache.Entry{}, false
	}
	entry, ok := f.opts.Cache.Get(key)
	if !ok || entry.Method != req.method || entry.URL != req.url {
		return cache.Entry{}, false
	}
	if req.check != nil && req.check(entry.Body) != nil {
		return cache.Entry{}, false
	}
	return entry, true
}
