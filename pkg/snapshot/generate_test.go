// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package snapshot_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/adoptium"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/endoflife"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/githubreleases"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/gradleservices"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/mavenmeta"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/rustchannel"
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
	adoptiumPath = "/v3/info/available_releases"
	deltaPath    = "/org/example/delta/maven-metadata.xml"
	deltaID      = "org.example:delta"
	gradlePath   = "/versions/all"
	rustPath     = "/dist/channel-rust-stable.toml"
	graphqlPath  = "/graphql"
	etaRepo      = "astral-sh/uv"
	thetaRepo    = "helm/helm"
	helm431      = "v4.3.1"
)

// twoTools is a catalog of two tools with their own endoflife products
// and one shared versions manifest, a third reading Adoptium, a fourth
// reading Maven Central metadata, a fifth reading Gradle services and
// a sixth reading the Rust stable channel, and two more reading GitHub
// releases: eta reads astral-sh/uv and theta reads helm/helm, the
// repositories of the recorded GraphQL response.
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
  gamma:
    name: Gamma
    version_scheme: major
    matrix_key: gamma-version
    sources:
      - {type: adoptium}
    policy: {rule: all, admit: installable}
  delta:
    name: Delta
    version_scheme: major.minor
    matrix_key: delta-version
    sources:
      - {type: maven-metadata, artifact: "org.example:delta"}
    policy: {rule: all, admit: installable}
  epsilon:
    name: Epsilon
    version_scheme: major
    matrix_key: epsilon-version
    sources:
      - {type: gradle-services}
    policy: {rule: all, admit: installable}
  zeta:
    name: Zeta
    version_scheme: major.minor
    matrix_key: zeta-version
    sources:
      - {type: rust-channel}
    policy: {rule: all, admit: installable}
  eta:
    name: Eta
    version_scheme: major.minor
    matrix_key: eta-version
    sources:
      - {type: github-releases, repository: astral-sh/uv}
    policy: {rule: all}
  theta:
    name: Theta
    version_scheme: major
    matrix_key: theta-version
    sources:
      - {type: github-releases, repository: helm/helm}
    policy: {rule: all}
`

var routes = map[string]string{
	alphaPath: `{"schema_version":"1.2.1","result":{"name":"alpha","releases":[
{"name":"2.1","releaseDate":"2026-01-02","isLts":false,"isEoas":false,"eoasFrom":null,"isEol":false,"eolFrom":"2028-01-02","latest":{"name":"2.1.3","date":"2026-09-01"}},
{"name":"2.0","releaseDate":"2025-01-02","isEol":true,"eolFrom":"2026-06-01","isEoes":false,"eoesFrom":"2027-06-01"},
{"name":"1","releaseDate":"2001-01-02","isEol":true}]}}`,
	betaPath: `{"schema_version":"1.2.1","result":{"name":"beta","releases":[
{"name":"1.0","isLts":true}]}}`,
	sharedPath: `[
{"version":"2.2.0-rc.1","stable":false,"files":[]},
{"version":"2.1.3","stable":true,"files":[]},
{"version":"2.1.10","stable":true,"files":[]},
{"version":"2.0.9","stable":true,"files":[]}]`,
	adoptiumPath: `{"available_lts_releases":[17,21],"available_releases":[17,21,9]}`,
	deltaPath: `<metadata><versioning><versions>
<version>1.0</version><version>1.1.0-M1</version><version>1.1.0</version><version>1.1.2</version>
<version>2.0.0-alpha-1</version><version>2.0.0-rc-1</version><version>2.0.0-SNAPSHOT</version>
</versions></versioning></metadata>`,
	gradlePath: `[
{"version":"3.1.0-rc-1","rcFor":"3.1.0","checksumUrl":"https://example.test/3.1.0-rc-1.sha256"},
{"version":"3.0.1","checksumUrl":"https://example.test/3.0.1.sha256"},
{"version":"3.0.2","broken":true,"checksumUrl":"https://example.test/3.0.2.sha256"},
{"version":"3.0","checksumUrl":"https://example.test/3.0.sha256"},
{"version":"2.9","checksumUrl":"https://example.test/2.9.sha256"},
{"version":"3.1.0-20261105051641+0000","snapshot":true,"nightly":true}]`,
	rustPath: "date = \"2026-10-15\"\n[pkg.rust]\nversion = \"1.99.1 (58cf8f9c3 2026-10-13)\"\n",
}

// recordedReleases is the GitHub GraphQL response recorded on
// 2026-11-06 for astral-sh/uv (r0) and helm/helm (r1), the aliases
// the query gives them in sorted order; its provenance is in the
// githubreleases tests that share it.
const recordedReleases = "../source/githubreleases/testdata/releases-recorded.json"

func init() {
	body, err := os.ReadFile(recordedReleases)
	if err != nil {
		panic(err)
	}
	routes[graphqlPath] = string(body)
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

func (u *upstream) clients(opts source.Options) (*endoflife.Client, *actionsmanifest.Client, *adoptium.Client, *mavenmeta.Client, *gradleservices.Client, *rustchannel.Client, *githubreleases.Client) {
	opts.Sleep = func(context.Context, time.Duration) error { return nil }
	opts.Retries = -1
	opts.Now = func() time.Time { return fixedNow }
	f := source.New(opts)
	eol := endoflife.New(f)
	eol.BaseURL = u.srv.URL + "/"
	m := actionsmanifest.New(f)
	m.BaseURL = u.srv.URL + "/"
	a := adoptium.New(f)
	a.BaseURL = u.srv.URL + "/"
	mm := mavenmeta.New(f)
	mm.BaseURL = u.srv.URL + "/"
	g := gradleservices.New(f)
	g.BaseURL = u.srv.URL + "/"
	r := rustchannel.New(f)
	r.BaseURL = u.srv.URL + "/"
	gh := githubreleases.New(f, "test-token")
	gh.BaseURL = u.srv.URL + "/"
	return eol, m, a, mm, g, r, gh
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
	eol, m, a, mm, g, r, gh := u.clients(source.Options{})
	s, err := snapshot.Generate(t.Context(), twoToolCatalog(t), eol, m, a, mm, g, r, gh, clock)
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

// stampAt returns the RFC 3339 timestamp s.
func stampAt(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
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
	adoptiumLines, _, ok := s.AdoptiumLines()
	if want := []string{"9", "17", "21"}; !ok || !slices.Equal(adoptiumLines, want) {
		t.Errorf("AdoptiumLines() = %v, %v; want %v", adoptiumLines, ok, want)
	}
	mavenLines, _, ok := s.MavenLines(deltaID)
	wantMaven := map[string]actionsmanifest.Line{
		"1.0":  {NewestStable: "1.0"},
		"1.1":  {NewestStable: "1.1.2", NewestPrerelease: "1.1.0-M1"},
		line20: {NewestPrerelease: "2.0.0-rc-1"},
	}
	if !ok || !reflect.DeepEqual(mavenLines, wantMaven) {
		t.Errorf("MavenLines(delta) = %+v, %v; want %+v", mavenLines, ok, wantMaven)
	}
	gradleLines, _, ok := s.GradleLines()
	wantGradle := map[string]gradleservices.Line{
		"3": {NewestStable: "3.0.1", ChecksumURL: "https://example.test/3.0.1.sha256", Broken: []string{"3.0.2"}},
		"2": {NewestStable: "2.9", ChecksumURL: "https://example.test/2.9.sha256"},
	}
	if !ok || !reflect.DeepEqual(gradleLines, wantGradle) {
		t.Errorf("GradleLines() = %+v, %v; want %+v", gradleLines, ok, wantGradle)
	}
	version, date, fetchedAt, ok := s.RustStable()
	if !ok || version != "1.99.1" || !date.Equal(*day("2026-10-15")) || !fetchedAt.Equal(stamp) {
		t.Errorf("RustStable() = %q, %v, %v, %v; want 1.99.1 of 2026-10-15 fetched at %v", version, date, fetchedAt, ok, stamp)
	}
	if err := s.Check(twoToolCatalog(t)); err != nil {
		t.Errorf("Check: %v", err)
	}
}

func TestGenerateGitHubReleases(t *testing.T) {
	s, u := generate(t, routes)
	stamp := fixedNow.Truncate(time.Second)
	if n := u.hits[graphqlPath]; n != 1 {
		t.Errorf("GraphQL queried %d times, want 1 for both repositories", n)
	}
	etaLines, fetchedAt, ok := s.GitHubLines(etaRepo)
	wantEta := map[string]githubreleases.Line{
		"0.13": {Released: day("2026-10-09"), NewestStable: "0.13.7", NewestStablePublished: stampAt("2026-11-03T02:45:04Z")},
		"0.12": {Released: day("2026-10-03"), NewestStable: "0.12.24", NewestStablePublished: stampAt("2026-10-08T20:06:20Z")},
	}
	if !ok || !fetchedAt.Equal(stamp) || !reflect.DeepEqual(etaLines, wantEta) {
		t.Errorf("GitHubLines(%s) = %+v, %v, %v; want %+v", etaRepo, etaLines, fetchedAt, ok, wantEta)
	}
	// Under the major scheme 4.1 to 4.3 make line 4, dated by v4.2.3,
	// its first stable release, and 3.20 to 3.22 make line 3; each keeps
	// its newest pre-release, v4.3.0-rc.1 and v3.22.0-rc.1.
	thetaLines, _, ok := s.GitHubLines(thetaRepo)
	wantTheta := map[string]githubreleases.Line{
		"4": {Released: day("2026-07-09"), NewestStable: helm431, NewestStablePublished: stampAt("2026-10-15T01:08:37Z"),
			NewestPrerelease: "v4.3.0-rc.1", NewestPrereleasePublished: stampAt("2026-08-31T22:09:46Z")},
		"3": {Released: day("2026-07-09"), NewestStable: "v3.22.1", NewestStablePublished: stampAt("2026-10-15T17:02:01Z"),
			NewestPrerelease: "v3.22.0-rc.1", NewestPrereleasePublished: stampAt("2026-08-31T22:56:05Z")},
	}
	if !ok || !reflect.DeepEqual(thetaLines, wantTheta) {
		t.Errorf("GitHubLines(%s) = %+v, %v; want %+v", thetaRepo, thetaLines, ok, wantTheta)
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
	eol, m, a, mm, g, r, gh := u.clients(source.Options{})
	s, err := snapshot.Generate(t.Context(), twoToolCatalog(t), eol, m, a, mm, g, r, gh, clock)
	if !errors.Is(err, source.ErrNotFound) || s != nil {
		t.Errorf("Generate with a 404 = %v, %v; want ErrNotFound and no snapshot", s, err)
	}

	eol, m, a, mm, g, r, gh = u.clients(source.Options{Offline: true})
	s, err = snapshot.Generate(t.Context(), twoToolCatalog(t), eol, m, a, mm, g, r, gh, clock)
	if !errors.Is(err, source.ErrNoData) || s != nil {
		t.Errorf("Generate offline = %v, %v; want ErrNoData and no snapshot", s, err)
	}
}

func TestGenerateNeedsGitHubToken(t *testing.T) {
	u := newUpstream(t, routes)
	eol, m, a, mm, g, r, gh := u.clients(source.Options{})
	gh.Token = ""
	s, err := snapshot.Generate(t.Context(), twoToolCatalog(t), eol, m, a, mm, g, r, gh, clock)
	if !errors.Is(err, githubreleases.ErrNoToken) || s != nil {
		t.Errorf("Generate without a token = %v, %v; want ErrNoToken and no snapshot", s, err)
	}
	if n := u.hits[graphqlPath]; n != 0 {
		t.Errorf("GraphQL queried %d times without a token", n)
	}
}
