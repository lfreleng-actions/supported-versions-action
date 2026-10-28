// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package endoflife

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

const (
	fixtureETag    = `"fixture"`
	productPython  = "python"
	pythonPath     = "/products/python/"
	nodeReleasePth = "/products/nodejs/releases/20/"
)

// feedDay is the day the fixtures were captured.
var feedDay = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

// upstream imitates endoflife.date: it serves fixtures by path with an
// ETag, answers a matching If-None-Match with 304 and an empty body,
// and answers anything else with 404.
type upstream struct {
	srv    *httptest.Server
	hits   atomic.Int32
	routes map[string][]byte
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func newUpstream(t *testing.T, routes map[string][]byte) *upstream {
	t.Helper()
	u := &upstream{routes: routes}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		body, ok := u.routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", fixtureETag)
		if r.Header.Get("If-None-Match") == fixtureETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) client(t *testing.T, opts source.Options) *Client {
	t.Helper()
	opts.Sleep = func(context.Context, time.Duration) error { return nil }
	c := New(source.New(opts))
	c.BaseURL = u.srv.URL + "/"
	return c
}

func TestReleasesPython(t *testing.T) {
	u := newUpstream(t, map[string][]byte{pythonPath: fixture(t, "python.json")})
	facts, res, err := u.client(t, source.Options{}).Releases(t.Context(), productPython)
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if res.Source != source.OriginLive {
		t.Errorf("Source = %q, want live", res.Source)
	}
	want := []struct {
		line, latest string
		status       release.Status
	}{
		{"3.10", "3.10.22", release.StatusEOL},
		{"3.13", "3.13.18", release.StatusSecurity},
		{"3.15", "3.15.2", release.StatusActive},
	}
	if len(facts) != len(want) {
		t.Fatalf("got %d lines, want %d", len(facts), len(want))
	}
	for i, w := range want {
		f := facts[i]
		if f.Line != w.line || f.Latest != w.latest {
			t.Errorf("facts[%d] = %s latest %s, want %s latest %s", i, f.Line, f.Latest, w.line, w.latest)
		}
		if f.IsEOES != nil || f.EOESFrom != nil {
			t.Errorf("%s: IsEOES/EOESFrom = %v/%v, want nil", f.Line, f.IsEOES, f.EOESFrom)
		}
		if got := release.StatusOf(f, feedDay); got != w.status {
			t.Errorf("%s: StatusOf = %q, want %q", f.Line, got, w.status)
		}
	}
	f := facts[2]
	if f.Released == nil || !f.Released.Equal(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("3.15 Released = %v", f.Released)
	}
	if f.LatestDate == nil || !f.LatestDate.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("3.15 LatestDate = %v", f.LatestDate)
	}
	if f.LTS == nil || *f.LTS || f.IsEOL == nil || *f.IsEOL {
		t.Errorf("3.15 LTS/IsEOL = %v/%v, want false/false", f.LTS, f.IsEOL)
	}
}

func TestReleaseNode(t *testing.T) {
	u := newUpstream(t, map[string][]byte{nodeReleasePth: fixture(t, "nodejs-20.json")})
	f, _, err := u.client(t, source.Options{}).Release(t.Context(), "nodejs", "20")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if f.Line != "20" || f.Latest != "20.20.2" {
		t.Errorf("Line/Latest = %s/%s, want 20/20.20.2", f.Line, f.Latest)
	}
	if f.LTS == nil || !*f.LTS {
		t.Errorf("LTS = %v, want true", f.LTS)
	}
	if f.IsEOES == nil || *f.IsEOES || f.EOESFrom != nil {
		t.Errorf("IsEOES/EOESFrom = %v/%v, want false/nil", f.IsEOES, f.EOESFrom)
	}
	if got := release.StatusOf(f, feedDay); got != release.StatusExtended {
		t.Errorf("StatusOf = %q, want extended", got)
	}
}

func TestRevalidationUsesETag(t *testing.T) {
	u := newUpstream(t, map[string][]byte{pythonPath: fixture(t, "python.json")})
	c, err := cache.New(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := c.Close(); cerr != nil {
			t.Errorf("Close: %v", cerr)
		}
	})
	now := feedDay
	opts := source.Options{Cache: c, Now: func() time.Time { return now }}
	if _, _, err = u.client(t, opts).Releases(t.Context(), productPython); err != nil {
		t.Fatal(err)
	}
	now = now.Add(7 * time.Hour)
	facts, res, err := u.client(t, opts).Releases(t.Context(), productPython)
	if err != nil {
		t.Fatalf("Releases after 304: %v", err)
	}
	if len(facts) != 3 || res.Source != source.OriginLive || !res.FetchedAt.Equal(now) {
		t.Errorf("after 304: %d lines, Source %q, FetchedAt %v", len(facts), res.Source, res.FetchedAt)
	}
	if u.hits.Load() != 2 {
		t.Errorf("requests = %d, want 2", u.hits.Load())
	}
}

func TestNotFound(t *testing.T) {
	u := newUpstream(t, nil)
	c := u.client(t, source.Options{})
	if _, _, err := c.Releases(t.Context(), "no-such-product"); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("unknown product: err = %v, want ErrNotFound", err)
	}
	if _, _, err := c.Release(t.Context(), productPython, "2.1"); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("unknown line: err = %v, want ErrNotFound", err)
	}
}

func TestSchemaVersion(t *testing.T) {
	body := string(fixture(t, "python.json"))
	for _, v := range []string{"2.0.0", "0.9", ""} {
		u := newUpstream(t, map[string][]byte{
			pythonPath: []byte(strings.Replace(body, `"1.2.1"`, `"`+v+`"`, 1)),
		})
		_, _, err := u.client(t, source.Options{}).Releases(t.Context(), productPython)
		if err == nil || !strings.Contains(err.Error(), `"`+v+`"`) {
			t.Errorf("schema_version %q: err = %v, want one naming it", v, err)
		}
	}
	u := newUpstream(t, map[string][]byte{
		pythonPath: []byte(strings.Replace(body, `"1.2.1"`, `"1.9"`, 1)),
	})
	if _, _, err := u.client(t, source.Options{}).Releases(t.Context(), productPython); err != nil {
		t.Errorf("schema_version 1.9: %v", err)
	}
}

func TestMalformedDate(t *testing.T) {
	body := string(fixture(t, "nodejs-20.json"))
	u := newUpstream(t, map[string][]byte{
		nodeReleasePth: []byte(strings.Replace(body, `"2026-04-30"`, `"30/04/2026"`, 1)),
	})
	_, _, err := u.client(t, source.Options{}).Release(t.Context(), "nodejs", "20")
	if err == nil || !strings.Contains(err.Error(), "eolFrom") {
		t.Errorf("err = %v, want one naming eolFrom", err)
	}
}

func TestInvalidNamesRejectedBeforeRequest(t *testing.T) {
	u := newUpstream(t, nil)
	c := u.client(t, source.Options{})
	for _, p := range []string{"", "Python", "-python", "py/thon", "..", "python?x"} {
		if _, _, err := c.Releases(t.Context(), p); err == nil {
			t.Errorf("Releases(%q) accepted an invalid product", p)
		}
		if _, _, err := c.Release(t.Context(), p, "3.13"); err == nil {
			t.Errorf("Release(%q, 3.13) accepted an invalid product", p)
		}
	}
	for _, l := range []string{"", ".", "..", ".13", "3/13", "3.13?x", "3 13"} {
		if _, _, err := c.Release(t.Context(), productPython, l); err == nil {
			t.Errorf("Release(python, %q) accepted an invalid line", l)
		}
	}
	if u.hits.Load() != 0 {
		t.Errorf("requests = %d, want 0", u.hits.Load())
	}
}

func TestDefaultBaseURL(t *testing.T) {
	c := New(source.New(source.Options{Offline: true}))
	_, _, err := c.Releases(t.Context(), productPython)
	if !errors.Is(err, source.ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData offline", err)
	}
	if want := "https://endoflife.date/api/v1/products/python/"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want one naming %s", err, want)
	}
}
