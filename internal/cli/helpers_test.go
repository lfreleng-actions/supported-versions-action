// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	catalogdata "github.com/lfreleng-actions/supported-versions-action/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
)

const (
	eolPrefix = "/eol/"
	rawPrefix = "/raw/"
	reportURL = "https://github.com/lfreleng-actions/supported-versions-action/catalog/schema/report.schema.json"
)

// fixedNow is the clock every test starts from.
var fixedNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// upstream serves endoflife.date and versions manifest responses built
// from the embedded snapshot, as pkg/versions' tests do.
type upstream struct {
	t      *testing.T
	srv    *httptest.Server
	bodies map[string][]byte

	mu sync.Mutex
	// failAll answers every request with this status when non-zero.
	failAll int
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	snap := embeddedSnapshot(t)
	u := &upstream{t: t, bodies: map[string][]byte{}}
	for product, p := range snap.EndOfLife {
		releases := make([]apiRelease, 0, len(p.Releases))
		for _, f := range p.Releases {
			r := toAPI(f)
			releases = append(releases, r)
			u.bodies[eolPrefix+"products/"+product+"/releases/"+f.Line+"/"] = envelope(t, r)
		}
		u.bodies[eolPrefix+"products/"+product+"/"] = envelope(t, map[string]any{"name": product, "releases": releases})
	}
	for repo, m := range snap.ActionsManifest {
		lines := make([]string, 0, len(m.Lines))
		for l := range m.Lines {
			lines = append(lines, l)
		}
		slices.SortFunc(lines, release.CompareLines)
		builds := []apiBuild{}
		for _, l := range slices.Backward(lines) {
			b := m.Lines[l]
			if b.NewestPrerelease != "" {
				builds = append(builds, apiBuild{Version: b.NewestPrerelease, Stable: false, Files: []any{}})
			}
			if b.NewestStable != "" {
				builds = append(builds, apiBuild{Version: b.NewestStable, Stable: true, Files: []any{}})
			}
		}
		data, err := json.Marshal(builds)
		if err != nil {
			t.Fatal(err)
		}
		u.bodies[rawPrefix+repo+"/main/versions-manifest.json"] = data
	}
	u.srv = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) serve(w http.ResponseWriter, req *http.Request) {
	u.mu.Lock()
	status := u.failAll
	u.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	body, ok := u.bodies[req.URL.Path]
	if !ok {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(body); err != nil {
		u.t.Errorf("writing the response: %v", err)
	}
}

func (u *upstream) setFailAll(status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failAll = status
}

// apiRelease is the endoflife.date v1 form of release.Facts.
type apiRelease struct {
	Name        string     `json:"name"`
	ReleaseDate *string    `json:"releaseDate,omitempty"`
	IsLTS       *bool      `json:"isLts,omitempty"`
	IsEOAS      *bool      `json:"isEoas,omitempty"`
	EOASFrom    *string    `json:"eoasFrom,omitempty"`
	IsEOL       *bool      `json:"isEol,omitempty"`
	EOLFrom     *string    `json:"eolFrom,omitempty"`
	IsEOES      *bool      `json:"isEoes,omitempty"`
	EOESFrom    *string    `json:"eoesFrom,omitempty"`
	Latest      *apiLatest `json:"latest,omitempty"`
}

type apiLatest struct {
	Name string  `json:"name"`
	Date *string `json:"date,omitempty"`
}

type apiBuild struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
	Files   []any  `json:"files"`
}

func toAPI(f release.Facts) apiRelease {
	r := apiRelease{
		Name:        f.Line,
		ReleaseDate: day(f.Released),
		IsLTS:       f.LTS,
		IsEOAS:      f.IsEOAS,
		EOASFrom:    day(f.EOASFrom),
		IsEOL:       f.IsEOL,
		EOLFrom:     day(f.EOLFrom),
		IsEOES:      f.IsEOES,
		EOESFrom:    day(f.EOESFrom),
	}
	if f.Latest != "" {
		r.Latest = &apiLatest{Name: f.Latest, Date: day(f.LatestDate)}
	}
	return r
}

func day(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.DateOnly)
	return &s
}

func envelope(t *testing.T, result any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"schema_version": "1.2.0", "result": result})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func embeddedSnapshot(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	s, err := snapshot.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// harness runs the command line against an upstream with the fixed
// clock, its own cache and no retries.
type harness struct {
	t        *testing.T
	u        *upstream
	hooks    hooks
	vars     map[string]string
	cacheDir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	u := newUpstream(t)
	return &harness{
		t: t,
		u: u,
		hooks: hooks{
			endOfLifeBaseURL: u.srv.URL + eolPrefix,
			manifestBaseURL:  u.srv.URL + rawPrefix,
			now:              func() time.Time { return fixedNow },
			snapshot:         embeddedSnapshot(t),
		},
		vars:     map[string]string{},
		cacheDir: t.TempDir(),
	}
}

// result is the outcome of one run.
type result struct {
	code   int
	stdout string
	stderr string
}

// run runs args after the harness's cache and retry flags.
func (h *harness) run(args ...string) result {
	h.t.Helper()
	return h.runRaw(append([]string{"--cache-dir", h.cacheDir, "--retries", "0"}, args...)...)
}

// runRaw runs args as given.
func (h *harness) runRaw(args ...string) result {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	e := &env{
		stdout: &stdout,
		stderr: &stderr,
		getenv: func(k string) string { return h.vars[k] },
		hooks:  h.hooks,
	}
	code := run(context.Background(), args, e)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func (r result) want(t *testing.T, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
}

var reportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(catalogdata.ReportSchema))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(reportURL, doc); err != nil {
		return nil, err
	}
	return c.Compile(reportURL)
})

// validReport checks data against report.schema.json and decodes it.
func validReport(t *testing.T, data []byte) map[string]any {
	t.Helper()
	sch, err := reportSchema()
	if err != nil {
		t.Fatalf("compile report schema: %v", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode report: %v\n%s", err, data)
	}
	if err := sch.Validate(inst); err != nil {
		t.Fatalf("report does not validate: %v\n%s", err, data)
	}
	m, ok := inst.(map[string]any)
	if !ok {
		t.Fatalf("report is not an object:\n%s", data)
	}
	return m
}

func wantContains(t *testing.T, what, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Errorf("%s lacks %q:\n%s", what, s, got)
		}
	}
}
