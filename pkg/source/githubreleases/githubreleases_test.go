// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package githubreleases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// The primary fixtures are GitHub GraphQL responses recorded on
// 2026-11-06 from api.github.com/graphql with an authenticated
// gh api graphql, sending the query query([]string{"astral-sh/uv",
// "helm/helm"}, nil) built before it asked for pageInfo. Each
// repository's nodes were then trimmed to fixture size, keeping the
// API's order and every field: r0 (astral-sh/uv) holds its newest 10
// releases, whose tags carry no "v", and r1 (helm/helm) its newest 8
// stable and 4 pre-releases, the 3.x and 4.x lines interleaved as
// GitHub returns them. not-found-recorded.json is the answer, an HTTP
// 200, for the absent repository notFoundRepo. A response without
// pageInfo is read as the last page.
//
// releases.json is hand-built, for edge cases the recorded data lacks:
// a draft, a release with no publishedAt and a non-version tag. The
// pagination tests build their pages inline for the same reason.
const (
	recordedFixture  = "releases-recorded.json"
	notFoundFixture  = "not-found-recorded.json"
	syntheticFixture = "releases.json"

	token        = "test-token"
	uvRepo       = "astral-sh/uv"
	helmRepo     = "helm/helm"
	notFoundRepo = "lfreleng-actions/no-such-repository-for-fixture"
	toolRepo     = "example-org/example-tool"
	otherRepo    = "example-org/other-tool"
	newest21     = "v2.1.10"
	newestUV     = "0.13.7"
	newestHelm4  = "v4.3.1"
	newestHelm3  = "v3.22.1"
	v100         = "1.0.0"
	line09       = "0.9"
	rc091        = "v0.9.0-rc.1"
	rc092        = "v0.9.0-rc.2"
)

// serve imitates the GitHub GraphQL endpoint, answering each query
// with what answer returns for its body. It checks the method, path
// and credential of each request. hits counts the requests.
func serve(t *testing.T, answer func(payload) []byte, opts source.Options) (*Client, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/graphql" {
			t.Errorf("request = %s %s, want POST /graphql", r.Method, r.URL.EscapedPath())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		var p payload
		b, err := io.ReadAll(r.Body)
		if err == nil {
			err = json.Unmarshal(b, &p)
		}
		if err != nil {
			t.Errorf("reading the request: %v", err)
		}
		if _, err := w.Write(answer(p)); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	opts.Sleep = func(context.Context, time.Duration) error { return nil }
	c := New(source.New(opts), token)
	c.BaseURL = srv.URL
	return c, hits
}

// upstream is serve answering every query with body, recording the
// last query in last.
func upstream(t *testing.T, body []byte, opts source.Options) (*Client, *atomic.Int32, *payload) {
	t.Helper()
	last := new(payload)
	c, hits := serve(t, func(p payload) []byte {
		*last = p
		return body
	}, opts)
	return c, hits, last
}

func newCache(t *testing.T) *cache.Cache {
	t.Helper()
	ca, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ca.Close(); err != nil {
			t.Error(err)
		}
	})
	return ca
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// day returns midnight UTC of a day in 2026, the year of every fixture.
func day(m time.Month, d int) time.Time {
	return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC)
}

// at returns a time of a day in 2026, in UTC.
func at(m time.Month, d, h, mi, sec int) time.Time {
	return time.Date(2026, m, d, h, mi, sec, 0, time.UTC)
}

// recorded returns the recorded releases of uv and Helm.
func recorded(t *testing.T) map[string][]Release {
	t.Helper()
	c, _, _ := upstream(t, fixture(t, recordedFixture), source.Options{})
	got, _, err := c.Releases(t.Context(), []string{helmRepo, uvRepo})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReleasesFromRecording(t *testing.T) {
	c, hits, last := upstream(t, fixture(t, recordedFixture), source.Options{})
	got, res, err := c.Releases(t.Context(), []string{helmRepo, uvRepo, helmRepo})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if res.Source != source.OriginLive {
		t.Errorf("Source = %q, want live", res.Source)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("requests = %d, want 1 batched request", n)
	}
	want := map[string]string{"o0": "astral-sh", "n0": "uv", "o1": "helm", "n1": "helm"}
	if len(last.Variables) != len(want) {
		t.Errorf("variables = %v, want %v", last.Variables, want)
	}
	for k, v := range want {
		if last.Variables[k] != v {
			t.Errorf("variable %s = %q, want %q", k, last.Variables[k], v)
		}
	}
	for _, part := range []string{
		"r0: repository(owner: $o0, name: $n0)", "r1: repository(owner: $o1, name: $n1)",
		"pageInfo { hasNextPage endCursor }",
	} {
		if !strings.Contains(last.Query, part) {
			t.Errorf("query lacks %q:\n%s", part, last.Query)
		}
	}
	if strings.Contains(last.Query, "after:") {
		t.Errorf("first page query names a cursor:\n%s", last.Query)
	}
	uv, helm := got[uvRepo], got[helmRepo]
	if len(uv) != 10 || uv[0].Tag != newestUV || uv[0].Prerelease || !uv[0].Published.Equal(time.Date(2026, 11, 3, 2, 45, 4, 0, time.UTC)) {
		t.Errorf("%s releases = %+v, want 10 from %s", uvRepo, uv, newestUV)
	}
	if len(helm) != 12 || helm[0].Tag != newestHelm3 || helm[1].Tag != newestHelm4 || !helm[4].Prerelease || helm[4].Tag != "v3.22.0-rc.1" {
		t.Errorf("%s releases = %+v, want 12 in the API's order", helmRepo, helm)
	}
}

func TestReleasesSkipsDrafts(t *testing.T) {
	// The synthetic fixture holds a draft with no publishedAt, which
	// the recorded data lacks.
	c, _, _ := upstream(t, fixture(t, syntheticFixture), source.Options{})
	got, _, err := c.Releases(t.Context(), []string{otherRepo, toolRepo})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if n := len(got[toolRepo]); n != 8 {
		t.Errorf("%s releases = %d, want 8 (the draft left out)", toolRepo, n)
	}
	if n := len(got[otherRepo]); n != 1 {
		t.Errorf("%s releases = %d, want 1", otherRepo, n)
	}
}

// pages answers a paged query: the first page of example-tool holds
// v2.1.0, published 2026-01-02, and names cursor "c1"; the page after
// "c1" holds v2.0.0, published a day earlier, and is the last. Every
// other repository has one page holding 1.0.0.
func pages(t *testing.T, queries *[]payload) func(payload) []byte {
	return func(p payload) []byte {
		*queries = append(*queries, p)
		var b strings.Builder
		b.WriteString(`{"data":{`)
		for i := 0; p.Variables[fmt.Sprintf("o%d", i)] != ""; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			repo := p.Variables[fmt.Sprintf("o%d", i)] + "/" + p.Variables[fmt.Sprintf("n%d", i)]
			tag, info := v100, `{"hasNextPage":false,"endCursor":"x"}`
			switch {
			case repo == toolRepo && p.Variables[fmt.Sprintf("a%d", i)] == "":
				tag, info = "v2.1.0", `{"hasNextPage":true,"endCursor":"c1"}`
			case repo == toolRepo && p.Variables[fmt.Sprintf("a%d", i)] == "c1":
				tag = "v2.0.0"
			case repo == toolRepo:
				t.Errorf("unexpected cursor in %v", p.Variables)
			}
			fmt.Fprintf(&b, `"r%d":{"releases":{"nodes":[{"tagName":%q,"isDraft":false,"isPrerelease":false,"publishedAt":"2026-01-0%dT00:00:00Z"}],"pageInfo":%s}}`,
				i, tag, 3-len(*queries), info)
		}
		b.WriteString(`}}`)
		return []byte(b.String())
	}
}

func TestReleasesReadsEveryPage(t *testing.T) {
	var queries []payload
	c, hits := serve(t, pages(t, &queries), source.Options{})
	got, res, err := c.Releases(t.Context(), []string{otherRepo, toolRepo})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if hits.Load() != 2 || res.Source != source.OriginLive {
		t.Fatalf("requests = %d, Source = %q; want 2 live pages", hits.Load(), res.Source)
	}
	// The second query reads only the repository with more, after its
	// cursor.
	second := queries[1]
	if second.Variables["o0"] != "example-org" || second.Variables["n0"] != "example-tool" ||
		second.Variables["a0"] != "c1" || second.Variables["o1"] != "" ||
		!strings.Contains(second.Query, "releases(first: 100, after: $a0,") {
		t.Errorf("second query = %+v", second)
	}
	tool := got[toolRepo]
	if len(tool) != 2 || tool[0].Tag != "v2.1.0" || tool[1].Tag != "v2.0.0" {
		t.Errorf("%s releases = %+v, want both pages, newest first", toolRepo, tool)
	}
	if len(got[otherRepo]) != 1 {
		t.Errorf("%s releases = %+v", otherRepo, got[otherRepo])
	}
	if l := Lines(tool, release.SchemeMajor)["2"]; l.Released == nil || !l.Released.Equal(day(1, 1)) {
		t.Errorf("line 2 = %+v, want it dated by v2.0.0 on the second page", l)
	}
}

func TestReleasesPagesShareOneBudget(t *testing.T) {
	// Each page takes 600ms, within the 1s a request may take, but the
	// two pages together outrun the one budget they share: the second
	// times out and the read falls back, here to source.ErrNoData.
	var queries []payload
	answer := pages(t, &queries)
	slow := func(p payload) []byte {
		time.Sleep(600 * time.Millisecond)
		return answer(p)
	}
	c, hits := serve(t, slow, source.Options{Timeout: time.Second, Retries: -1})
	_, _, err := c.Releases(t.Context(), []string{toolRepo})
	if !errors.Is(err, source.ErrNoData) {
		t.Errorf("err = %v, want source.ErrNoData", err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

func TestReleasesRejectsEndlessHistory(t *testing.T) {
	endless := func(payload) []byte {
		return []byte(`{"data":{"r0":{"releases":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"more"}}}}}`)
	}
	c, hits := serve(t, endless, source.Options{})
	_, _, err := c.Releases(t.Context(), []string{toolRepo})
	if err == nil || !strings.Contains(err.Error(), "the history would be incomplete") || !strings.Contains(err.Error(), toolRepo) {
		t.Errorf("err = %v, want an incomplete history naming %s", err, toolRepo)
	}
	if n := hits.Load(); n != MaxPages {
		t.Errorf("requests = %d, want %d", n, MaxPages)
	}
}

func TestReleasesCachedAcrossTokens(t *testing.T) {
	c, hits, _ := upstream(t, fixture(t, recordedFixture), source.Options{Cache: newCache(t)})
	if _, _, err := c.Releases(t.Context(), []string{uvRepo, helmRepo}); err != nil {
		t.Fatal(err)
	}
	c2 := New(c.Fetcher, "another-token")
	c2.BaseURL = c.BaseURL
	_, res, err := c2.Releases(t.Context(), []string{helmRepo, uvRepo})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != source.OriginCache || hits.Load() != 1 {
		t.Errorf("Source = %q after %d requests, want one request then the cache", res.Source, hits.Load())
	}
}

func TestReleasesNoToken(t *testing.T) {
	c, hits, _ := upstream(t, fixture(t, recordedFixture), source.Options{})
	c.Token = ""
	_, _, err := c.Releases(t.Context(), []string{uvRepo})
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	for _, s := range []string{Type, TokenEnv, uvRepo} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not name %q", err, s)
		}
	}
	if hits.Load() != 0 {
		t.Error("called the API without a token")
	}
}

func TestReleasesOfflineWithoutToken(t *testing.T) {
	c, hits, _ := upstream(t, fixture(t, recordedFixture), source.Options{Offline: true})
	c.Token = ""
	_, _, err := c.Releases(t.Context(), []string{uvRepo})
	if !errors.Is(err, source.ErrNoData) {
		t.Errorf("err = %v, want source.ErrNoData", err)
	}
	if hits.Load() != 0 {
		t.Error("called the API offline")
	}
}

func TestReleasesNone(t *testing.T) {
	c, hits, _ := upstream(t, fixture(t, recordedFixture), source.Options{})
	got, _, err := c.Releases(t.Context(), nil)
	if err != nil || len(got) != 0 || hits.Load() != 0 {
		t.Errorf("Releases(nil) = %v, %v after %d requests", got, err, hits.Load())
	}
}

func TestReleasesBadName(t *testing.T) {
	c, hits, _ := upstream(t, fixture(t, recordedFixture), source.Options{})
	_, _, err := c.Releases(t.Context(), []string{"not-a-repo"})
	if err == nil || !strings.Contains(err.Error(), "not an owner/name name") {
		t.Errorf("err = %v, want a bad name error", err)
	}
	if hits.Load() != 0 {
		t.Error("called the API for a bad name")
	}
}

func TestReleasesRecordedNotFound(t *testing.T) {
	c, hits, _ := upstream(t, fixture(t, notFoundFixture), source.Options{Cache: newCache(t)})
	for range 2 {
		_, _, err := c.Releases(t.Context(), []string{notFoundRepo})
		if err == nil || !strings.Contains(err.Error(), "GraphQL errors: repository "+notFoundRepo+": NOT_FOUND") {
			t.Fatalf("err = %v, want NOT_FOUND naming %s", err, notFoundRepo)
		}
	}
	if hits.Load() != 2 {
		t.Errorf("requests = %d, want 2: a rejected answer is not cached", hits.Load())
	}
}

func TestReleasesRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"graphql errors", `{"data":{"r0":null},"errors":[{"type":"NOT_FOUND","path":["r0"],"message":"Could not resolve to a Repository"}]}`, "repository " + toolRepo + ": NOT_FOUND"},
		{"rate limit beside a final error", `{"data":{"r0":null},"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"},{"type":"NOT_FOUND","path":["r0"],"message":"Could not resolve to a Repository"}]}`, "repository " + toolRepo + ": NOT_FOUND"},
		{"unplaced error", `{"errors":[{"message":"Something went wrong"}]}`, "GraphQL errors: Something went wrong"},
		{"missing repository", `{"data":{}}`, "no data for repository " + toolRepo},
		{"next page without cursor", `{"data":{"r0":{"releases":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":""}}}}}`, "a next page with no cursor"},
		{"malformed", `{`, "unexpected end"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, hits, _ := upstream(t, []byte(tt.body), source.Options{Cache: newCache(t)})
			for range 2 {
				_, _, err := c.Releases(t.Context(), []string{toolRepo})
				if err == nil || !strings.Contains(err.Error(), tt.want) || errors.Is(err, source.ErrTransient) {
					t.Fatalf("err = %v, want a final one containing %q", err, tt.want)
				}
			}
			if hits.Load() != 2 {
				t.Errorf("requests = %d, want 2: a rejected answer is not cached", hits.Load())
			}
		})
	}
}

// rateLimitedBody is the form of GitHub's GraphQL answer, an HTTP 200,
// to a query sent once the primary rate limit is spent. It is
// hand-built: recording one needs the limit exhausted.
const rateLimitedBody = `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded for user ID 1."}]}`

func TestReleasesRateLimitedRetries(t *testing.T) {
	var n atomic.Int32
	body := fixture(t, recordedFixture)
	c, hits := serve(t, func(payload) []byte {
		if n.Add(1) == 1 {
			return []byte(rateLimitedBody)
		}
		return body
	}, source.Options{Cache: newCache(t)})
	got, res, err := c.Releases(t.Context(), []string{uvRepo})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != source.OriginLive || len(got[uvRepo]) == 0 || hits.Load() != 2 {
		t.Errorf("Source = %q, %d releases after %d requests; want live releases after a retry",
			res.Source, len(got[uvRepo]), hits.Load())
	}
}

func TestReleasesRateLimitedFallsBack(t *testing.T) {
	var limited atomic.Bool
	body := fixture(t, recordedFixture)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	c, hits := serve(t, func(payload) []byte {
		if limited.Load() {
			return []byte(rateLimitedBody)
		}
		return body
	}, source.Options{Cache: newCache(t), Retries: 1, Now: func() time.Time { return now }})
	repos := []string{uvRepo, helmRepo}
	if _, _, err := c.Releases(t.Context(), repos); err != nil {
		t.Fatal(err)
	}

	// Rate limited with an expired entry: retried, then served stale.
	limited.Store(true)
	now = now.Add(source.DefaultMaxAge + time.Hour)
	got, res, err := c.Releases(t.Context(), repos)
	if err != nil {
		t.Fatal(err)
	}
	if res.Source != source.OriginStale || len(got[uvRepo]) == 0 || len(res.Warnings) != 1 ||
		!strings.Contains(res.Warnings[0], "RATE_LIMITED") {
		t.Errorf("Source = %q, warnings %q; want the cached releases, stale, naming the rate limit", res.Source, res.Warnings)
	}
	if hits.Load() != 3 {
		t.Errorf("requests = %d, want 1 then 2", hits.Load())
	}

	// With nothing cached, no data, so the resolver can use the snapshot.
	_, _, err = c.Releases(t.Context(), []string{uvRepo})
	if !errors.Is(err, source.ErrNoData) || !errors.Is(err, source.ErrTransient) || !strings.Contains(err.Error(), "RATE_LIMITED") {
		t.Errorf("err = %v, want source.ErrNoData naming RATE_LIMITED", err)
	}
}

func TestLinesRecorded(t *testing.T) {
	got := recorded(t)
	uv := Lines(got[uvRepo], release.SchemeMajorMinor)
	if len(uv) != 2 {
		t.Errorf("uv lines = %+v, want 0.12 and 0.13", uv)
	}
	if l := uv["0.13"]; l.NewestStable != newestUV || !l.NewestStablePublished.Equal(at(11, 3, 2, 45, 4)) || !l.Released.Equal(day(10, 9)) {
		t.Errorf("uv 0.13 = %+v, want newest %s at 2026-11-03T02:45:04Z, released 2026-10-09", l, newestUV)
	}
	if l := uv["0.12"]; l.NewestStable != "0.12.24" || !l.Released.Equal(day(10, 3)) || l.NewestPrerelease != "" {
		t.Errorf("uv 0.12 = %+v", l)
	}

	helm := Lines(got[helmRepo], release.SchemeMajorMinor)
	if len(helm) != 6 {
		t.Errorf("helm lines = %+v, want 3.20, 3.21, 3.22, 4.1, 4.2 and 4.3", helm)
	}
	if l := helm["4.3"]; l.NewestStable != newestHelm4 || !l.NewestStablePublished.Equal(at(10, 15, 1, 8, 37)) ||
		!l.Released.Equal(day(9, 9)) || l.NewestPrerelease != "v4.3.0-rc.1" || !l.NewestPrereleasePublished.Equal(at(8, 31, 22, 9, 46)) {
		t.Errorf("helm 4.3 = %+v", l)
	}
	if l := helm["3.22"]; l.NewestStable != newestHelm3 || !l.Released.Equal(day(9, 10)) {
		t.Errorf("helm 3.22 = %+v, want newest %s, released 2026-09-10", l, newestHelm3)
	}
	for _, line := range []string{"3.20", "4.1"} {
		if l := helm[line]; l.NewestStable != "" || l.Released != nil || l.NewestPrerelease != "v"+line+".0-rc.1" {
			t.Errorf("helm %s = %+v, want only a pre-release", line, l)
		}
	}
	major := Lines(got[helmRepo], release.SchemeMajor)
	if len(major) != 2 || major["4"].NewestStable != newestHelm4 || major["3"].NewestStable != newestHelm3 {
		t.Errorf("helm major lines = %+v", major)
	}
}

func TestLinesSyntheticEdgeCases(t *testing.T) {
	// The synthetic fixture holds what the recorded data lacks: a
	// non-version tag, "nightly", which is skipped, and a draft.
	c, _, _ := upstream(t, fixture(t, syntheticFixture), source.Options{})
	got, _, err := c.Releases(t.Context(), []string{toolRepo})
	if err != nil {
		t.Fatal(err)
	}
	lines := Lines(got[toolRepo], release.SchemeMajorMinor)
	if len(lines) != 3 {
		t.Errorf("lines = %v, want 2.0, 2.1 and 2.2", lines)
	}
	l := lines["2.1"]
	if l.NewestStable != newest21 || l.NewestStablePublished == nil || !l.NewestStablePublished.Equal(at(2, 20, 12, 0, 0)) {
		t.Errorf("2.1 newest = %q at %v, want %s at 2026-02-20T12:00:00Z", l.NewestStable, l.NewestStablePublished, newest21)
	}
	if l.Released == nil || !l.Released.Equal(day(1, 10)) {
		t.Errorf("2.1 released = %v, want 2026-01-10, its first stable release", l.Released)
	}
	if l := lines["2.2"]; l.NewestStable != "" || l.Released != nil || l.NewestPrerelease != "v2.2.0-rc.1" {
		t.Errorf("2.2 = %+v, want only a pre-release", l)
	}
}

func TestLinesTieBreaksOnPublication(t *testing.T) {
	early, late := day(1, 1), day(1, 2)
	got := Lines([]Release{
		{Tag: v100, Published: late},
		{Tag: "v1.0.0", Published: early},
	}, release.SchemeMajorMinor)
	if l := got["1.0"]; l.NewestStable != v100 || l.Released == nil || !l.Released.Equal(early) {
		t.Errorf("1.0 = %+v, want the later 1.0.0 newest and released on the earlier day", l)
	}
}

func TestFacts(t *testing.T) {
	lines := Lines(recorded(t)[helmRepo], release.SchemeMajorMinor)

	all := Facts(lines)
	var names []string
	for _, f := range all {
		names = append(names, f.Line)
	}
	if strings.Join(names, " ") != "3.20 3.21 3.22 4.1 4.2 4.3" {
		t.Fatalf("facts = %+v, want 3.20, 3.21, 3.22, 4.1, 4.2 and 4.3", all)
	}
	if !all[0].Prerelease || !all[3].Prerelease {
		t.Errorf("facts = %+v, want 3.20 and 4.1 marked pre-releases", all)
	}
	if f := all[5]; f.Latest != newestHelm4 || f.Released == nil || !f.Released.Equal(day(9, 9)) ||
		f.LatestDate == nil || !f.LatestDate.Equal(day(10, 15)) || f.Prerelease {
		t.Errorf("4.3 facts = %+v", f)
	}
}

func TestMergeLines(t *testing.T) {
	jul, aug, sep := day(7, 1), day(8, 1), day(9, 1)
	into := map[string]Line{
		"0.8": {Released: &aug, NewestStable: "v0.8.2", NewestStablePublished: &sep, NewestPrerelease: "v0.8.3-rc.1", NewestPrereleasePublished: &sep},
		"0.7": {Released: &jul, NewestStable: "v0.7.1", NewestStablePublished: &jul},
	}
	MergeLines(into, map[string]Line{
		"0.8":  {Released: &jul, NewestStable: "v0.8.1", NewestStablePublished: &aug, NewestPrerelease: "v0.8.4-rc.1", NewestPrereleasePublished: &jul},
		line09: {NewestPrerelease: rc091, NewestPrereleasePublished: &aug},
	})
	if l := into["0.8"]; *l.Released != jul || l.NewestStable != "v0.8.2" || *l.NewestStablePublished != sep ||
		l.NewestPrerelease != "v0.8.4-rc.1" || *l.NewestPrereleasePublished != jul {
		t.Errorf("0.8 = %+v, want released %v, newest v0.8.2 and v0.8.4-rc.1", l, jul)
	}
	if l := into["0.7"]; l.NewestStable != "v0.7.1" {
		t.Errorf("0.7 = %+v, want it kept", l)
	}
	if l := into[line09]; l.NewestPrerelease != rc091 || l.Released != nil {
		t.Errorf("0.9 = %+v, want it added", l)
	}
	if !Newer("v0.8.10", "0.8.9") || Newer("v0.8.2", "0.8.2") {
		t.Error("Newer does not order by version number")
	}
}

func TestMergeLinesPrereleaseTieBreaksOnPublication(t *testing.T) {
	// rc.1 and rc.2 of one version, a month apart and hours apart on
	// one day: the one published later is newer, as in Lines,
	// whichever repository comes first.
	for name, times := range map[string][2]time.Time{
		"days apart":  {day(9, 1), day(10, 1)},
		"same day":    {at(9, 1, 9, 0, 0), at(9, 1, 17, 0, 0)},
		"same minute": {at(9, 1, 9, 0, 1), at(9, 1, 9, 0, 2)},
	} {
		rc1 := Line{NewestPrerelease: rc091, NewestPrereleasePublished: &times[0]}
		rc2 := Line{NewestPrerelease: rc092, NewestPrereleasePublished: &times[1]}
		for _, tt := range []struct{ into, from Line }{{rc1, rc2}, {rc2, rc1}} {
			lines := map[string]Line{line09: tt.into}
			MergeLines(lines, map[string]Line{line09: tt.from})
			if l := lines[line09]; l.NewestPrerelease != rc092 || !l.NewestPrereleasePublished.Equal(times[1]) {
				t.Errorf("%s: merging %s into %s = %+v, want %s", name, tt.from.NewestPrerelease, tt.into.NewestPrerelease, l, rc092)
			}
		}
	}
}

func TestMergeLinesAgreesWithLinesOnOneDay(t *testing.T) {
	// Two repositories publish rc.1 at 09:00 and rc.2 at 17:00 on one
	// day. Merging their lines, in either order, gives what Lines
	// gives over both repositories' releases.
	a := []Release{{Tag: rc091, Prerelease: true, Published: at(9, 1, 9, 0, 0)}}
	b := []Release{{Tag: rc092, Prerelease: true, Published: at(9, 1, 17, 0, 0)}}
	want := Lines(append(slices.Clone(a), b...), release.SchemeMajorMinor)[line09]
	if want.NewestPrerelease != rc092 {
		t.Fatalf("Lines over both = %+v, want %s", want, rc092)
	}
	for name, order := range map[string][2][]Release{"rc.1 first": {a, b}, "rc.2 first": {b, a}} {
		lines := Lines(order[0], release.SchemeMajorMinor)
		MergeLines(lines, Lines(order[1], release.SchemeMajorMinor))
		got := lines[line09]
		if got.NewestPrerelease != want.NewestPrerelease || !got.NewestPrereleasePublished.Equal(*want.NewestPrereleasePublished) {
			t.Errorf("%s: merged = %+v, want %+v", name, got, want)
		}
	}
}
