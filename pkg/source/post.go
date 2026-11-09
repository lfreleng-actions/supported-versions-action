// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package source

import (
	"context"
	"net/http"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
)

// Post sends body to url with header and returns the response through
// the cache, following the steps Get does, except that an expired
// entry is not revalidated: the Post is sent without conditional
// headers, which a server holding a matching ETag would answer with
// 412, and the entry is served stale only when retries run out. The
// cache key covers the method, the URL and the body, never header, so
// credentials stay out of both the key and the entry. check, when not
// nil, vets the body of a 200 response before it is stored or served:
// a body it rejects is not cached and fails the Post with check's
// error, without a retry unless that error wraps ErrTransient, which is
// retried and then served a cached entry, or fails with ErrNoData, as
// a 429 is. It also vets every cached entry this Post would serve: one
// it rejects is ignored, as if absent.
//
// Unlike concurrent Gets, concurrent Posts never share a request: each
// caller's check governs its own request, retries and cache fallback,
// so no caller is answered by a decision another caller's check made.
// A Post returns ctx.Err() once ctx ends, and is otherwise bounded by
// Timeout and the retry budget, as the fetch Gets share is. A ctx from
// Budget whose budget is spent is not an end but a budget run out: the
// Post serves a fresh entry, or falls back as its retries ran out.
func (f *Fetcher) Post(ctx context.Context, url string, body []byte, header http.Header, check func([]byte) error) (Result, error) {
	if err := ctx.Err(); err != nil && !spent(ctx) {
		return Result{}, err
	}
	req := request{method: http.MethodPost, url: url, body: body, header: header, check: check}
	ctx, cancel := context.WithTimeoutCause(ctx, f.budget(), errBudget)
	defer cancel()
	res, err := f.get(ctx, cache.Key(req.method, req.url, req.body), req, f.opts.MaxAge)
	return res.clone(), err
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
