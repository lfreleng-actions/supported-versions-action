// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

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
	"github.com/lfreleng-actions/supported-versions-action/pkg/cache"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	eolPrefix      = "/eol/"
	rawPrefix      = "/raw/"
	pythonList     = "/eol/products/python/"
	goList         = "/eol/products/go/"
	pythonManifest = "/raw/actions/python-versions/main/versions-manifest.json"
	reportURL      = "https://github.com/lfreleng-actions/supported-versions-action/catalog/schema/report.schema.json"
)

// fixedNow is the clock every test starts from.
var fixedNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// upstream serves endoflife.date and versions manifest responses built
// from the embedded snapshot, and counts the requests for each path.
type upstream struct {
	t      *testing.T
	srv    *httptest.Server
	bodies map[string][]byte

	mu     sync.Mutex
	counts map[string]int
	status map[string]int
	// failAll answers every request with this status when non-zero.
	failAll int
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	snap := embeddedSnapshot(t)
	u := &upstream{t: t, bodies: map[string][]byte{}, counts: map[string]int{}, status: map[string]int{}}
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
	u.counts[req.URL.Path]++
	status := u.status[req.URL.Path]
	if u.failAll != 0 {
		status = u.failAll
	}
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

func (u *upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.counts[path]
}

func (u *upstream) total() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, c := range u.counts {
		n += c
	}
	return n
}

func (u *upstream) setStatus(path string, status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status[path] = status
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

// newResolver returns a resolver pointed at u with the fixed clock, the
// embedded snapshot, its own cache and no retries. edit, when not nil,
// adjusts the options first.
func newResolver(t *testing.T, u *upstream, edit func(*versions.Options)) *versions.Resolver {
	t.Helper()
	c, err := cache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := c.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	opts := versions.Options{
		Snapshot:         embeddedSnapshot(t),
		Now:              func() time.Time { return fixedNow },
		EndOfLifeBaseURL: u.srv.URL + eolPrefix,
		ManifestBaseURL:  u.srv.URL + rawPrefix,
	}
	opts.Fetch.Cache = c
	opts.Fetch.Retries = -1
	opts.Fetch.Sleep = func(context.Context, time.Duration) error { return nil }
	if edit != nil {
		edit(&opts)
	}
	r, err := versions.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return r
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

// validated encodes rep, checks it against report.schema.json and
// returns the encoding.
func validated(t *testing.T, rep versions.Report) []byte {
	t.Helper()
	sch, err := reportSchema()
	if err != nil {
		t.Fatalf("compile report schema: %v", err)
	}
	data, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("}\n")) || !bytes.Contains(data, []byte("\n  \"schema\": 1,")) {
		t.Errorf("JSON is not two-space indented with a trailing newline:\n%s", data)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Errorf("report does not validate: %v\n%s", err, data)
	}
	return data
}

func lineOf(t *testing.T, rep versions.Report, line string) versions.Line {
	t.Helper()
	for _, l := range rep.Lines {
		if l.Line == line {
			return l
		}
	}
	t.Fatalf("report has no line %s", line)
	return versions.Line{}
}

func wantVersions(t *testing.T, rep versions.Report, want ...string) {
	t.Helper()
	if !slices.Equal(rep.Matrix.Versions, want) {
		t.Errorf("matrix versions = %v, want %v", rep.Matrix.Versions, want)
	}
}

func hasWarning(rep versions.Report, substr string) bool {
	return slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.Contains(w, substr) })
}
