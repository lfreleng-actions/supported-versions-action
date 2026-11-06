// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/githubreleases"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	githubPrefix = "/github/"
	graphqlPath  = githubPrefix + "graphql"
	uvTool       = "uv"
	uvRepo       = "astral-sh/uv"
	helmTool     = "helm"
	synthRepo    = "example-org/uvlike"
	synthMirror  = "example-org/uvlike-mirror"
	fixedTool    = "fixed"
	line08       = "0.8"
	line012      = "0.12"
	line013      = "0.13"
	newestUV     = "0.13.7"
	snapUV       = "0.13.3"
	v082         = "v0.8.2"
)

// recordedReleases is the GitHub GraphQL response recorded on
// 2026-11-06 for astral-sh/uv (r0) and helm/helm (r1), trimmed; its
// provenance is in the githubreleases tests that share it.
const recordedReleases = "../source/githubreleases/testdata/releases-recorded.json"

// sourcesCatalog holds uv and Helm read from GitHub releases, a tool
// reading two repositories that both publish one line, a static tool
// with two lines, a tool whose static line a repository also
// publishes, and a tool whose static line sits between two
// repositories in catalog order.
//
//nolint:misspell // eol_behaviour is spelled as the catalog field
const sourcesCatalog = `schema: 1
tools:
  uv:
    name: uv
    version_scheme: major.minor
    matrix_key: uv-version
    sources:
      - {type: github-releases, repository: astral-sh/uv}
    policy: {rule: all, admit: installable}
  helm:
    name: Helm
    version_scheme: major.minor
    matrix_key: helm-version
    sources:
      - {type: github-releases, repository: helm/helm}
    policy: {rule: all, admit: installable}
  mirrored:
    name: Mirrored
    version_scheme: major.minor
    matrix_key: mirrored-version
    sources:
      - {type: github-releases, repository: example-org/uvlike-mirror}
      - {type: github-releases, repository: example-org/uvlike}
    policy: {rule: all}
  fixed:
    name: Fixed
    version_scheme: major.minor
    matrix_key: fixed-version
    sources:
      - type: static
        lines:
          - {line: "1.0", released: "2024-02-01", eol: "2026-02-01"}
          - {line: "1.1", released: "2025-03-01", eol: "2028-03-01", lts: true}
    policy: {rule: all, eol_behaviour: strip}
  pinned:
    name: Pinned
    version_scheme: major.minor
    matrix_key: pinned-version
    sources:
      - type: static
        lines:
          - {line: "0.8", released: "2026-07-01", eol: "2027-07-01"}
      - {type: github-releases, repository: example-org/uvlike}
    policy: {rule: all}
  interleaved:
    name: Interleaved
    version_scheme: major.minor
    matrix_key: interleaved-version
    sources:
      - {type: github-releases, repository: example-org/uvlike-old}
      - type: static
        lines:
          - {line: "0.8", released: "2026-07-01", eol: "2027-07-01"}
      - {type: github-releases, repository: example-org/uvlike}
    policy: {rule: all}
`

// synthReleases answers the query for example-org/uvlike alone as r0,
// and for example-org/uvlike and example-org/uvlike-mirror as r0 and
// r1, in sorted order. It is hand-built: the recorded repositories share
// no line, and these tests need repositories that do.
const synthReleases = `{"data":{
"r0":{"releases":{"nodes":[
{"tagName":"v1.0.0","isDraft":true,"isPrerelease":false,"publishedAt":null},
{"tagName":"v0.9.0-rc.1","isDraft":false,"isPrerelease":true,"publishedAt":"2026-10-01T00:00:00Z"},
{"tagName":"v0.8.2","isDraft":false,"isPrerelease":false,"publishedAt":"2026-09-01T10:00:00Z"},
{"tagName":"v0.8.0","isDraft":false,"isPrerelease":false,"publishedAt":"2026-08-01T10:00:00Z"},
{"tagName":"v0.7.5","isDraft":false,"isPrerelease":false,"publishedAt":"2026-05-01T10:00:00Z"}]}},
"r1":{"releases":{"nodes":[
{"tagName":"v0.8.3","isDraft":false,"isPrerelease":false,"publishedAt":"2026-09-15T10:00:00Z"},
{"tagName":"v0.8.0","isDraft":false,"isPrerelease":false,"publishedAt":"2026-07-15T10:00:00Z"},
{"tagName":"v0.6.0","isDraft":false,"isPrerelease":false,"publishedAt":"2026-01-01T10:00:00Z"}]}}}}`

// interleavedReleases answers the query for example-org/uvlike as r0
// and example-org/uvlike-old, which never published 0.8, as r1.
const interleavedReleases = `{"data":{
"r0":{"releases":{"nodes":[
{"tagName":"v0.8.2","isDraft":false,"isPrerelease":false,"publishedAt":"2026-09-01T10:00:00Z"},
{"tagName":"v0.8.0","isDraft":false,"isPrerelease":false,"publishedAt":"2026-08-01T10:00:00Z"}]}},
"r1":{"releases":{"nodes":[
{"tagName":"v0.7.0","isDraft":false,"isPrerelease":false,"publishedAt":"2026-04-01T10:00:00Z"},
{"tagName":"v0.6.0","isDraft":false,"isPrerelease":false,"publishedAt":"2026-01-01T10:00:00Z"}]}}}}`

// sourcesResolver returns a resolver for sourcesCatalog whose GitHub
// endpoint answers with the recorded response.
func sourcesResolver(t *testing.T, u *upstream, edit func(*versions.Options)) *versions.Resolver {
	t.Helper()
	body, err := os.ReadFile(recordedReleases)
	if err != nil {
		t.Fatal(err)
	}
	return graphqlResolver(t, u, body, edit)
}

// recordedHelm returns the recorded response with its helm/helm
// entry, r1, as r0, the alias a query for helm/helm alone gives it.
func recordedHelm(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(recordedReleases)
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(body, &rec); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(map[string]any{"data": map[string]json.RawMessage{"r0": rec.Data["r1"]}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// synthResolver is sourcesResolver answering with synthReleases.
func synthResolver(t *testing.T, u *upstream) *versions.Resolver {
	t.Helper()
	return graphqlResolver(t, u, []byte(synthReleases), nil)
}

func graphqlResolver(t *testing.T, u *upstream, body []byte, edit func(*versions.Options)) *versions.Resolver {
	t.Helper()
	cat, err := catalog.Parse([]byte(sourcesCatalog))
	if err != nil {
		t.Fatal(err)
	}
	u.bodies[graphqlPath] = body
	return newResolver(t, u, func(o *versions.Options) {
		o.Catalog = cat
		o.GitHubBaseURL = u.srv.URL + githubPrefix
		o.GitHubToken = "test-token"
		if edit != nil {
			edit(o)
		}
	})
}

func TestMatrixGitHubReleases(t *testing.T) {
	u := newUpstream(t)
	rep, err := sourcesResolver(t, u, nil).Matrix(context.Background(), uvTool, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, line012, line013)
	if rep.Latest != line013 {
		t.Errorf("latest = %q, want %s", rep.Latest, line013)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", rep.Warnings)
	}
	src := rep.Source.Sources
	if len(src) != 1 || src[0].Type != githubreleases.Type || src[0].Ref != uvRepo || src[0].Kind != versions.KindLive || src[0].Catalog {
		t.Errorf("sources = %+v", src)
	}
	l := lineOf(t, rep, line013)
	if !l.InMatrix || !l.Installable || l.Newest != newestUV || l.Released != "2026-10-09" {
		t.Errorf("%s = %+v, want installable at %s, released 2026-10-09", line013, l, newestUV)
	}
	if n := u.count(graphqlPath); n != 1 {
		t.Errorf("GraphQL requests = %d, want 1", n)
	}
}

func TestMatrixGitHubReleasesHelm(t *testing.T) {
	u := newUpstream(t)
	r := sourcesResolver(t, u, nil)
	u.bodies[graphqlPath] = recordedHelm(t)
	rep, err := r.Matrix(context.Background(), helmTool, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	// 3.20 and 4.1 have only pre-releases, which the policy does not
	// admit.
	wantVersions(t, rep, "3.21", "3.22", "4.2", "4.3")
	if l := lineOf(t, rep, "4.3"); !l.Installable || l.Newest != "v4.3.1" || l.Released != "2026-09-09" {
		t.Errorf("4.3 = %+v, want installable at v4.3.1, released 2026-09-09", l)
	}
	if l := lineOf(t, rep, "3.22"); !l.Installable || l.Newest != "v3.22.1" || l.Released != "2026-09-10" {
		t.Errorf("3.22 = %+v, want installable at v3.22.1, released 2026-09-10", l)
	}
}

func TestMatrixGitHubReleasesPrerelease(t *testing.T) {
	u := newUpstream(t)
	r := sourcesResolver(t, u, func(o *versions.Options) {
		o.Override = &catalog.PolicyOverride{Prerelease: ptr(true)}
	})
	u.bodies[graphqlPath] = recordedHelm(t)
	rep, err := r.Matrix(context.Background(), helmTool, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	for _, line := range []string{"3.20", "4.1"} {
		if l := lineOf(t, rep, line); l.Status != release.StatusPrerelease || !l.InMatrix {
			t.Errorf("%s = %+v, want a pre-release in the matrix", line, l)
		}
	}
}

func TestMatrixGitHubReleasesBatched(t *testing.T) {
	u := newUpstream(t)
	rep, err := synthResolver(t, u).Matrix(context.Background(), "mirrored", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if n := u.count(graphqlPath); n != 1 {
		t.Errorf("GraphQL requests = %d, want 1 for both repositories", n)
	}
	src := rep.Source.Sources
	if len(src) != 2 || src[0].Ref != synthMirror || src[1].Ref != synthRepo {
		t.Errorf("sources = %+v, want catalog order", src)
	}
	wantVersions(t, rep, "0.6", "0.7", line08)
	// Both repositories publish 0.8: the mirror, first in catalog
	// order, dates it, and the newest release of either is its build.
	if l := lineOf(t, rep, line08); !l.Installable || l.Newest != "v0.8.3" || l.Released != "2026-07-15" {
		t.Errorf("%s = %+v, want installable at v0.8.3, released 2026-07-15", line08, l)
	}
}

func TestMatrixGitHubReleasesKeepCatalogOrder(t *testing.T) {
	u := newUpstream(t)
	r := synthResolver(t, u)
	u.bodies[graphqlPath] = []byte(interleavedReleases)
	rep, err := r.Matrix(context.Background(), "interleaved", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if n := u.count(graphqlPath); n != 1 {
		t.Errorf("GraphQL requests = %d, want 1 for both repositories", n)
	}
	// example-org/uvlike-old, first, does not publish 0.8, so the
	// static line, second, dates it ahead of example-org/uvlike, third,
	// which supplies its newest release.
	l := lineOf(t, rep, line08)
	if !l.Installable || l.Newest != v082 || l.Released != "2026-07-01" || l.EOL != "2027-07-01" {
		t.Errorf("%s = %+v, want static dates and newest %s", line08, l, v082)
	}
	if l := lineOf(t, rep, "0.6"); l.Released != "2026-01-01" {
		t.Errorf("0.6 = %+v, want it from example-org/uvlike-old", l)
	}
}

func TestMatrixStaticAndGitHubReleases(t *testing.T) {
	u := newUpstream(t)
	rep, err := synthResolver(t, u).Matrix(context.Background(), "pinned", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "0.7", line08)
	// The static line, first in catalog order, keeps its dates; the
	// repository supplies the newest release the static line lacks.
	l := lineOf(t, rep, line08)
	if !l.Installable || l.Newest != v082 || l.Released != "2026-07-01" || l.EOL != "2027-07-01" {
		t.Errorf("%s = %+v, want static dates and newest v0.8.2", line08, l)
	}
	if src := rep.Source.Sources; len(src) != 2 || src[0].Type != snapshot.TypeStatic || src[1].Ref != synthRepo {
		t.Errorf("sources = %+v, want static then %s", src, synthRepo)
	}
}

func TestCheckGitHubReleases(t *testing.T) {
	u := newUpstream(t)
	rep, err := sourcesResolver(t, u, nil).Check(context.Background(), uvTool, "0.12.5")
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if len(rep.Lines) != 1 || rep.Lines[0].Line != line012 || rep.Lines[0].Newest != "0.12.24" {
		t.Errorf("lines = %+v, want only %s", rep.Lines, line012)
	}
}

func TestGitHubReleasesNoToken(t *testing.T) {
	u := newUpstream(t)
	r := sourcesResolver(t, u, func(o *versions.Options) { o.GitHubToken = "" })
	_, err := r.Matrix(context.Background(), uvTool, versions.MatrixRequest{})
	if !errors.Is(err, githubreleases.ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	for _, want := range []string{"github-releases", "GITHUB_TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if u.total() != 0 {
		t.Errorf("requests = %d, want none without a token", u.total())
	}
}

func TestGitHubReleasesOffline(t *testing.T) {
	u := newUpstream(t)
	snap := embeddedSnapshot(t)
	snap.GitHubReleases = map[string]snapshot.GitHubRepo{
		uvRepo: {FetchedAt: fixedNow.Add(-24 * time.Hour), Lines: map[string]githubreleases.Line{
			line013: {Released: ptr(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)), NewestStable: snapUV},
		}},
	}
	r := sourcesResolver(t, u, func(o *versions.Options) {
		o.Fetch.Offline = true
		o.GitHubToken = ""
		o.Snapshot = snap
	})
	rep, err := r.Matrix(context.Background(), uvTool, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, line013)
	if rep.Source.Kind != versions.KindSnapshot || lineOf(t, rep, line013).Newest != snapUV {
		t.Errorf("source = %+v, lines = %+v", rep.Source, rep.Lines)
	}

	_, err = r.Matrix(context.Background(), "mirrored", versions.MatrixRequest{})
	if !errors.Is(err, versions.ErrNoFacts) || !errors.Is(err, source.ErrNoData) || !strings.Contains(err.Error(), synthMirror) {
		t.Errorf("err = %v, want ErrNoFacts naming %s", err, synthMirror)
	}
	if u.total() != 0 {
		t.Errorf("requests = %d, want none offline", u.total())
	}
}

func TestGitHubReleasesRateLimitedUsesSnapshot(t *testing.T) {
	u := newUpstream(t)
	snap := embeddedSnapshot(t)
	snap.GitHubReleases = map[string]snapshot.GitHubRepo{
		uvRepo: {FetchedAt: fixedNow.Add(-24 * time.Hour), Lines: map[string]githubreleases.Line{
			line013: {Released: ptr(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)), NewestStable: snapUV},
		}},
	}
	// GitHub answers a query past the primary rate limit with HTTP 200.
	limited := []byte(`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded for user ID 1."}]}`)
	r := graphqlResolver(t, u, limited, func(o *versions.Options) { o.Snapshot = snap })
	rep, err := r.Matrix(context.Background(), uvTool, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, line013)
	if rep.Source.Kind != versions.KindSnapshot || lineOf(t, rep, line013).Newest != snapUV {
		t.Errorf("source = %+v, lines = %+v", rep.Source, rep.Lines)
	}
	if u.count(graphqlPath) != 1 {
		t.Errorf("requests = %d, want 1", u.count(graphqlPath))
	}
}

func TestGitHubReleasesSnapshotSameDayPrerelease(t *testing.T) {
	// Two repositories publish rc.1 at 09:00 and rc.2 at 17:00 on one
	// day. After a snapshot round trip, rc.2 is the line's build
	// whichever repository holds it.
	early := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	late := time.Date(2026, 9, 1, 17, 0, 0, 0, time.UTC)
	rc1 := githubreleases.Line{NewestPrerelease: "v0.9.0-rc.1", NewestPrereleasePublished: &early}
	rc2 := githubreleases.Line{NewestPrerelease: "v0.9.0-rc.2", NewestPrereleasePublished: &late}
	for name, order := range map[string][2]githubreleases.Line{"rc.1 first": {rc1, rc2}, "rc.2 first": {rc2, rc1}} {
		t.Run(name, func(t *testing.T) {
			u := newUpstream(t)
			snap := embeddedSnapshot(t)
			at := fixedNow.Add(-24 * time.Hour)
			snap.GitHubReleases = map[string]snapshot.GitHubRepo{
				synthMirror: {FetchedAt: at, Lines: map[string]githubreleases.Line{"0.9": order[0]}},
				synthRepo:   {FetchedAt: at, Lines: map[string]githubreleases.Line{"0.9": order[1]}},
			}
			data, err := snap.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if snap, err = snapshot.Parse(data); err != nil {
				t.Fatal(err)
			}
			r := graphqlResolver(t, u, []byte(synthReleases), func(o *versions.Options) {
				o.Fetch.Offline = true
				o.Snapshot = snap
				o.Override = &catalog.PolicyOverride{Prerelease: ptr(true)}
			})
			rep, err := r.Matrix(context.Background(), "mirrored", versions.MatrixRequest{})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			if l := lineOf(t, rep, "0.9"); l.Newest != "v0.9.0-rc.2" {
				t.Errorf("0.9 = %+v, want newest v0.9.0-rc.2", l)
			}
		})
	}
}

func TestMatrixStatic(t *testing.T) {
	u := newUpstream(t)
	rep, err := sourcesResolver(t, u, nil).Matrix(context.Background(), fixedTool, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	data := validated(t, rep)
	if !strings.Contains(string(data), `"catalog": true`) {
		t.Errorf("report does not mark the facts catalog-held:\n%s", data)
	}
	src := rep.Source.Sources
	if len(src) != 1 || src[0].Type != snapshot.TypeStatic || src[0].Ref != versions.StaticRef ||
		src[0].Kind != versions.KindLive || !src[0].Catalog || src[0].FetchedAt != nil {
		t.Errorf("sources = %+v", src)
	}
	wantVersions(t, rep, "1.1")
	if l := lineOf(t, rep, "1.0"); l.Status != release.StatusEOL || l.InMatrix || l.EOL != "2026-02-01" {
		t.Errorf("1.0 = %+v, want end of life and dropped", l)
	}
	if l := lineOf(t, rep, "1.1"); !l.LTS || l.Released != "2025-03-01" || l.EOL != "2028-03-01" {
		t.Errorf("1.1 = %+v", l)
	}
	if u.total() != 0 {
		t.Errorf("requests = %d, want none for a static tool", u.total())
	}

	rep, err = sourcesResolver(t, u, nil).Check(context.Background(), fixedTool, "1.1.4")
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if len(rep.Lines) != 1 || rep.Lines[0].Line != "1.1" || rep.Check == nil {
		t.Errorf("check = %+v, lines = %+v", rep.Check, rep.Lines)
	}
}
