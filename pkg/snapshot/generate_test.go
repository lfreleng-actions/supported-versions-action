// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package snapshot_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/endoflife"
)

const (
	alphaPath    = "/products/alpha/"
	betaPath     = "/products/beta/"
	sharedRepo   = "owner/shared-versions"
	sharedPath   = "/owner/shared-versions/main/versions-manifest.json"
	productAlpha = "alpha"
	productBeta  = "beta"
	line20       = "2.0"
	line21       = "2.1"
)

// twoTools is a catalog of two tools with their own endoflife products
// and one shared versions manifest.
const twoTools = `schema: 1
tools:
  alpha:
    name: Alpha
    version_scheme: major.minor
    matrix_key: alpha-version
    sources:
      - {type: endoflife, product: alpha}
      - {type: actions-manifest, repository: owner/shared-versions}
    policy: {rule: all, admit: installable}
  beta:
    name: Beta
    version_scheme: major.minor
    matrix_key: beta-version
    sources:
      - {type: endoflife, product: beta}
      - {type: actions-manifest, repository: owner/shared-versions}
      - {type: static, lines: [{line: "1.0"}]}
    policy: {rule: all, admit: installable}
`

var routes = map[string]string{
	alphaPath: `{"schema_version":"1.2.1","result":{"name":"alpha","releases":[
{"name":"2.1","releaseDate":"2026-01-02","isLts":false,"isEoas":false,"eoasFrom":null,"isEol":false,"eolFrom":"2028-01-02","latest":{"name":"2.1.3","date":"2026-09-01"}},
{"name":"2.0","releaseDate":"2025-01-02","isEol":true,"eolFrom":"2026-06-01","isEoes":false,"eoesFrom":"2027-06-01"}]}}`,
	betaPath: `{"schema_version":"1.2.1","result":{"name":"beta","releases":[
{"name":"1.0","isLts":true}]}}`,
	sharedPath: `[
{"version":"2.2.0-rc.1","stable":false,"files":[]},
{"version":"2.1.3","stable":true,"files":[]},
{"version":"2.1.10","stable":true,"files":[]},
{"version":"2.0.9","stable":true,"files":[]}]`,
}

// fixedNow is the clock every generation test uses.
var fixedNow = time.Date(2026, 10, 28, 21, 30, 0, 500, time.UTC)

// upstream serves routes for both clients and counts hits per path.
type upstream struct {
	mu   sync.Mutex
	hits map[string]int
	srv  *httptest.Server
}

func newUpstream(t *testing.T, routes map[string]string) *upstream {
	t.Helper()
	u := &upstream{hits: make(map[string]int)}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits[r.URL.EscapedPath()]++
		u.mu.Unlock()
		body, ok := routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) clients(opts source.Options) (*endoflife.Client, *actionsmanifest.Client) {
	opts.Sleep = func(context.Context, time.Duration) error { return nil }
	opts.Retries = -1
	opts.Now = func() time.Time { return fixedNow }
	f := source.New(opts)
	eol := endoflife.New(f)
	eol.BaseURL = u.srv.URL + "/"
	m := actionsmanifest.New(f)
	m.BaseURL = u.srv.URL + "/"
	return eol, m
}

func twoToolCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.Parse([]byte(twoTools))
	if err != nil {
		t.Fatalf("catalog.Parse: %v", err)
	}
	return cat
}

func clock() time.Time { return fixedNow }

func generate(t *testing.T, routes map[string]string) (*snapshot.Snapshot, *upstream) {
	t.Helper()
	u := newUpstream(t, routes)
	eol, m := u.clients(source.Options{})
	s, err := snapshot.Generate(t.Context(), twoToolCatalog(t), eol, m, clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return s, u
}

func day(s string) *time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &d
}

func ptr[T any](v T) *T { return &v }

func TestGenerate(t *testing.T) {
	s, u := generate(t, routes)
	stamp := fixedNow.Truncate(time.Second)
	if !s.GeneratedAt.Equal(stamp) || s.GeneratedAt.Location() != time.UTC {
		t.Errorf("GeneratedAt = %v, want %v", s.GeneratedAt, stamp)
	}
	facts, fetchedAt, ok := s.Facts(productAlpha)
	if !ok {
		t.Fatal("Facts(alpha) not found")
	}
	if !fetchedAt.Equal(stamp) {
		t.Errorf("alpha fetched_at = %v, want %v", fetchedAt, stamp)
	}
	want := []release.Facts{
		{Line: line20, Released: day("2025-01-02"), IsEOL: ptr(true), EOLFrom: day("2026-06-01"),
			IsEOES: ptr(false), EOESFrom: day("2027-06-01")},
		{Line: line21, Released: day("2026-01-02"), LTS: ptr(false), IsEOAS: ptr(false), IsEOL: ptr(false),
			EOLFrom: day("2028-01-02"), Latest: "2.1.3", LatestDate: day("2026-09-01")},
	}
	if !reflect.DeepEqual(facts, want) {
		t.Errorf("Facts(alpha) = %+v, want %+v", facts, want)
	}
	if facts, _, found := s.Facts(productBeta); !found || len(facts) != 1 || facts[0].LTS == nil || !*facts[0].LTS {
		t.Errorf("Facts(beta) = %+v, %v", facts, found)
	}
	lines, _, ok := s.Lines(sharedRepo)
	if !ok {
		t.Fatal("Lines(shared) not found")
	}
	wantLines := map[string]actionsmanifest.Line{
		"2.2":  {NewestPrerelease: "2.2.0-rc.1"},
		line21: {NewestStable: "2.1.10"},
		line20: {NewestStable: "2.0.9"},
	}
	if !reflect.DeepEqual(lines, wantLines) {
		t.Errorf("Lines(shared) = %+v, want %+v", lines, wantLines)
	}
	if n := u.hits[sharedPath]; n != 1 {
		t.Errorf("shared manifest fetched %d times, want 1", n)
	}
	if err := s.Check(twoToolCatalog(t)); err != nil {
		t.Errorf("Check: %v", err)
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a, _ := generate(t, routes)
	b, _ := generate(t, routes)
	da, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	db, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(da, db) {
		t.Errorf("two runs differ:\n%s\n%s", da, db)
	}
}

func TestGenerateFailsOnAnyFetchError(t *testing.T) {
	missing := make(map[string]string)
	for k, v := range routes {
		if k != betaPath {
			missing[k] = v
		}
	}
	u := newUpstream(t, missing)
	eol, m := u.clients(source.Options{})
	s, err := snapshot.Generate(t.Context(), twoToolCatalog(t), eol, m, clock)
	if !errors.Is(err, source.ErrNotFound) || s != nil {
		t.Errorf("Generate with a 404 = %v, %v; want ErrNotFound and no snapshot", s, err)
	}

	eol, m = u.clients(source.Options{Offline: true})
	s, err = snapshot.Generate(t.Context(), twoToolCatalog(t), eol, m, clock)
	if !errors.Is(err, source.ErrNoData) || s != nil {
		t.Errorf("Generate offline = %v, %v; want ErrNoData and no snapshot", s, err)
	}
}
