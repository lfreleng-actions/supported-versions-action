// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	python    = "python"
	python310 = "/eol/products/python/releases/3.10/"
	python313 = "/eol/products/python/releases/3.13/"
)

func overlay(t *testing.T, text string) *catalog.Overlay {
	t.Helper()
	o, err := catalog.ParseOverlay([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestMatrixPythonLive(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), "py", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.11", "3.12", "3.13", "3.14", "3.15")
	if rep.Latest != "3.15" {
		t.Errorf("latest = %q, want 3.15", rep.Latest)
	}
	if rep.Tool != python || rep.Name != "Python" || rep.Matrix.Key != "python-version" {
		t.Errorf("identity = %q %q %q", rep.Tool, rep.Name, rep.Matrix.Key)
	}
	if rep.Source.Kind != versions.KindLive || rep.Source.SnapshotDate != "" {
		t.Errorf("source = %+v, want live without a snapshot date", rep.Source)
	}
	if len(rep.Source.Sources) != 2 || rep.Source.Sources[0].Type != "endoflife" ||
		rep.Source.Sources[1].Ref != "actions/python-versions" {
		t.Errorf("sources = %+v, want endoflife then actions-manifest", rep.Source.Sources)
	}
	checkPythonLines(t, rep)
	if rep.Constraint != nil || rep.Check != nil {
		t.Errorf("constraint = %+v, check = %+v, want both nil", rep.Constraint, rep.Check)
	}
	if rep.Policy.Rule != catalog.RuleAll || rep.Policy.Floor != "3.11" || rep.Policy.Current != nil || rep.Policy.Overlay {
		t.Errorf("policy = %+v", rep.Policy)
	}
}

// checkPythonLines checks the lines of the live Python report.
func checkPythonLines(t *testing.T, rep versions.Report) {
	t.Helper()
	if l := lineOf(t, rep, "3.10"); l.InMatrix || l.Reason != policy.ReasonBelowFloor || l.Status != release.StatusEOL {
		t.Errorf("3.10 = %+v, want eol and below-floor", l)
	}
	l := lineOf(t, rep, "3.13")
	if !l.InMatrix || l.Status != release.StatusSecurity || l.Released != "2024-10-07" ||
		l.EOAS != "2026-10-01" || l.EOL != "2029-10-31" || l.Newest != "3.13.18" || !l.Installable ||
		l.DaysToEOL == nil || *l.DaysToEOL != 1099 {
		t.Errorf("3.13 = %+v", l)
	}
}

func TestMatrixGoLive(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), "golang", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "1.26", "1.27")
	if rep.Latest != "1.27" {
		t.Errorf("latest = %q, want 1.27", rep.Latest)
	}
}

func TestMatrixExplicitConstraint(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{Constraint: ">=3.12,<3.15"})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.12", "3.13", "3.14")
	if rep.Latest != "3.14" {
		t.Errorf("latest = %q, want 3.14", rep.Latest)
	}
	for _, line := range []string{"3.11", "3.15"} {
		if l := lineOf(t, rep, line); l.InMatrix || l.Reason != versions.ReasonConstraint {
			t.Errorf("%s = %+v, want reason constraint", line, l)
		}
	}
	if rep.Constraint == nil || rep.Constraint.Outcome != constraint.OutcomeApplied ||
		rep.Constraint.Text != ">=3.12,<3.15" || rep.Constraint.Origin != "" {
		t.Errorf("constraint = %+v", rep.Constraint)
	}
}

func TestMatrixDetectedConstraint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"),
		[]byte("[project]\nrequires-python = \">=3.13\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{ProjectDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.13", "3.14", "3.15")
	if rep.Constraint == nil || rep.Constraint.Origin != "pyproject.toml:project.requires-python" ||
		rep.Constraint.Text != ">=3.13" || rep.Constraint.Outcome != constraint.OutcomeApplied {
		t.Errorf("constraint = %+v", rep.Constraint)
	}

	// An explicit constraint wins over detection.
	rep, err = r.Matrix(context.Background(), python, versions.MatrixRequest{ProjectDir: dir, Constraint: ">=3.14"})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.14", "3.15")
}

func TestMatrixConstraintOutOfRange(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	req := versions.MatrixRequest{Constraint: "<3.0"}
	rep, err := r.Matrix(context.Background(), python, req)
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.11", "3.12", "3.13", "3.14", "3.15")
	if rep.Constraint == nil || rep.Constraint.Outcome != constraint.OutcomeOutOfRangeFallback {
		t.Errorf("constraint = %+v, want out-of-range-fallback", rep.Constraint)
	}
	if !hasWarning(rep, "allows no line") {
		t.Errorf("warnings = %q, want the fallback warning", rep.Warnings)
	}

	req.Strict = true
	rep, err = r.Matrix(context.Background(), python, req)
	if !errors.Is(err, versions.ErrConstraintOutOfRange) {
		t.Fatalf("err = %v, want ErrConstraintOutOfRange", err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.11", "3.12", "3.13", "3.14", "3.15")
}

func TestMatrixConstraintErrors(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	_, err := r.Matrix(context.Background(), python, versions.MatrixRequest{Constraint: ">=>3"})
	if !errors.Is(err, constraint.ErrSyntax) {
		t.Errorf("err = %v, want ErrSyntax", err)
	}

	cat, err := catalog.Parse([]byte(`schema: 1
tools:
  plain:
    name: Plain
    version_scheme: major.minor
    matrix_key: plain-version
    sources:
      - {type: endoflife, product: go}
    policy: {rule: all, admit: released}
`))
	if err != nil {
		t.Fatal(err)
	}
	r = newResolver(t, u, func(o *versions.Options) { o.Catalog = cat })
	if _, err := r.Matrix(context.Background(), "plain", versions.MatrixRequest{Constraint: ">=1.0"}); err == nil {
		t.Error("constraint for a tool without a constraint syntax: want an error")
	}
}

func TestMatrixCache(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	if _, err := r.Matrix(context.Background(), python, versions.MatrixRequest{}); err != nil {
		t.Fatal(err)
	}
	before := u.total()
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Source.Kind != versions.KindCache {
		t.Errorf("kind = %q, want cache", rep.Source.Kind)
	}
	if after := u.total(); after != before {
		t.Errorf("requests = %d after the second call, want %d", after, before)
	}
}

func TestMatrixOfflineSnapshot(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, func(o *versions.Options) { o.Fetch.Offline = true })
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Source.Kind != versions.KindSnapshot || rep.Source.SnapshotDate != "2026-10-28" {
		t.Errorf("source = %+v, want snapshot of 2026-10-28", rep.Source)
	}
	wantVersions(t, rep, "3.11", "3.12", "3.13", "3.14", "3.15")
	if hasWarning(rep, "days old") {
		t.Errorf("warnings = %q, want no age warning for a fresh snapshot", rep.Warnings)
	}
	if u.total() != 0 {
		t.Errorf("offline resolver made %d requests", u.total())
	}

	later := embeddedSnapshot(t).GeneratedAt.Add(45 * 24 * time.Hour)
	r = newResolver(t, u, func(o *versions.Options) {
		o.Fetch.Offline = true
		o.Now = func() time.Time { return later }
	})
	rep, err = r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if !hasWarning(rep, "45 days old") {
		t.Errorf("warnings = %q, want the snapshot age warning", rep.Warnings)
	}
}

func TestMatrixServerErrors(t *testing.T) {
	u := newUpstream(t)
	u.setFailAll(http.StatusInternalServerError)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Source.Kind != versions.KindSnapshot {
		t.Errorf("kind = %q, want snapshot", rep.Source.Kind)
	}
	wantVersions(t, rep, "3.11", "3.12", "3.13", "3.14", "3.15")

	snap := embeddedSnapshot(t)
	delete(snap.EndOfLife, python)
	r = newResolver(t, u, func(o *versions.Options) { o.Snapshot = snap })
	if _, err := r.Matrix(context.Background(), python, versions.MatrixRequest{}); !errors.Is(err, versions.ErrNoFacts) {
		t.Errorf("err = %v, want ErrNoFacts", err)
	}
}

func TestMatrixEndOfLifeNotFound(t *testing.T) {
	u := newUpstream(t)
	u.setStatus(pythonList, http.StatusNotFound)
	r := newResolver(t, u, nil)
	_, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err == nil || errors.Is(err, versions.ErrNoFacts) {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

func TestMatrixManifestUnavailable(t *testing.T) {
	u := newUpstream(t)
	u.setStatus(pythonManifest, http.StatusInternalServerError)
	snap := embeddedSnapshot(t)
	snap.ActionsManifest = map[string]snapshot.Manifest{}
	r := newResolver(t, u, func(o *versions.Options) { o.Snapshot = snap })
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if !hasWarning(rep, policy.WarnNoAvailability) {
		t.Errorf("warnings = %q, want the no-installability warning", rep.Warnings)
	}
	wantVersions(t, rep, "3.11", "3.12", "3.13", "3.14", "3.15")
	if s := rep.Source.Sources[1]; s.Kind != versions.KindUnavailable || s.FetchedAt != nil {
		t.Errorf("manifest source = %+v, want unavailable", s)
	}
}

func TestMatrixOverlay(t *testing.T) {
	u := newUpstream(t)
	const origin = "lfreleng-actions/.github@0123abc"
	r := newResolver(t, u, func(o *versions.Options) {
		o.Overlay = overlay(t, "schema: 1\ntools:\n  python:\n    policy:\n      floor: \"3.12\"\n")
		o.OverlayOrigin = origin
	})
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.12", "3.13", "3.14", "3.15")
	if !rep.Policy.Overlay || rep.Policy.Origin != origin || rep.Policy.Floor != "3.12" {
		t.Errorf("policy = %+v, want the overlay echoed", rep.Policy)
	}

	// The overlay does not touch Go.
	rep, err = r.Matrix(context.Background(), "go", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Policy.Overlay || rep.Policy.Origin != "" {
		t.Errorf("go policy = %+v, want no overlay", rep.Policy)
	}
}

func TestMatrixEOLFail(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, func(o *versions.Options) {
		o.Overlay = overlay(t, "schema: 1\ntools:\n  python:\n    policy:\n      eol_behaviour: fail\n") //nolint:misspell // field name fixed by the catalog format
		o.Now = func() time.Time { return time.Date(2027, 11, 1, 12, 0, 0, 0, time.UTC) }
	})
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if !errors.Is(err, policy.ErrEOL) {
		t.Fatalf("err = %v, want ErrEOL", err)
	}
	validated(t, rep)
	wantVersions(t, rep, "3.11", "3.12", "3.13", "3.14", "3.15")
	if l := lineOf(t, rep, "3.11"); l.Status != release.StatusEOL || !l.InMatrix {
		t.Errorf("3.11 = %+v, want eol and in the matrix", l)
	}
}

func TestMatrixLabels(t *testing.T) {
	cat, err := catalog.Parse([]byte(`schema: 1
tools:
  gochannel:
    name: Go channel
    version_scheme: major.minor
    matrix_key: go-version
    sources:
      - {type: endoflife, product: go}
    policy:
      rule: latest
      count: 2
      labels: [stable]
`))
	if err != nil {
		t.Fatal(err)
	}
	u := newUpstream(t)
	r := newResolver(t, u, func(o *versions.Options) { o.Catalog = cat })
	rep, err := r.Matrix(context.Background(), "gochannel", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "1.26", "1.27", "stable")
	if rep.Latest != "1.27" {
		t.Errorf("latest = %q, want 1.27", rep.Latest)
	}
	if rep.Policy.Count != 2 || len(rep.Policy.Labels) != 1 {
		t.Errorf("policy = %+v", rep.Policy)
	}
}

func TestCheck(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	ctx := context.Background()

	rep, err := r.Check(ctx, python, "3.10.22")
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	c := rep.Check
	if c == nil || c.Status != release.StatusEOL || c.Supported || c.InMatrix ||
		c.Reason != policy.ReasonBelowFloor || c.Line != "3.10" || c.Version != "3.10.22" ||
		c.EOL != "2026-10-01" || c.DaysToEOL == nil || *c.DaysToEOL != -27 {
		t.Errorf("check = %+v", c)
	}
	if u.count(python310) != 1 || u.count(pythonList) != 0 {
		t.Errorf("requests: single release %d, list %d; want 1 and 0", u.count(python310), u.count(pythonList))
	}

	rep, err = r.Check(ctx, python, "3.13.4")
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if c := rep.Check; !c.Supported || !c.InMatrix || c.Status != release.StatusSecurity {
		t.Errorf("check 3.13.4 = %+v, want supported", c)
	}
	wantVersions(t, rep, "3.13")

	rep, err = r.Check(ctx, python, "3.99")
	if err != nil {
		t.Fatalf("check 3.99: %v", err)
	}
	validated(t, rep)
	if c := rep.Check; c.Status != release.StatusUnknown || c.Supported || c.InMatrix {
		t.Errorf("check 3.99 = %+v, want unknown", c)
	}

	if _, err := r.Check(ctx, python, "stable"); !errors.Is(err, release.ErrNotAVersion) {
		t.Errorf("check stable: err = %v, want ErrNotAVersion", err)
	}
	if u.count(pythonList) != 0 {
		t.Errorf("checks fetched the full list %d times, want 0", u.count(pythonList))
	}
}

func TestCheckFullList(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, func(o *versions.Options) {
		o.Overlay = overlay(t, "schema: 1\ntools:\n  python:\n    policy:\n      rule: lts\n      current: true\n")
	})
	rep, err := r.Check(context.Background(), python, "3.13.4")
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if u.count(pythonList) != 1 || u.count(python313) != 0 {
		t.Errorf("requests: list %d, single release %d; want 1 and 0", u.count(pythonList), u.count(python313))
	}
	if rep.Policy.Current == nil || !*rep.Policy.Current {
		t.Errorf("policy = %+v, want current echoed", rep.Policy)
	}
	if c := rep.Check; c.InMatrix || c.Reason != policy.ReasonNotLTS || !c.PolicyExcluded {
		t.Errorf("check = %+v, want excluded as not-lts", c)
	}
}

func TestCheckSnapshot(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, func(o *versions.Options) { o.Fetch.Offline = true })
	rep, err := r.Check(context.Background(), python, "3.13.4")
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Source.Kind != versions.KindSnapshot || !rep.Check.Supported {
		t.Errorf("report = %+v, want a supported line from the snapshot", rep)
	}
	if len(rep.Lines) != 1 {
		t.Errorf("lines = %+v, want the checked line alone", rep.Lines)
	}
}

func TestErrors(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	ctx := context.Background()
	if _, err := r.Matrix(ctx, "cobol", versions.MatrixRequest{}); !errors.Is(err, versions.ErrUnknownTool) {
		t.Errorf("matrix: err = %v, want ErrUnknownTool", err)
	}
	if _, err := r.Check(ctx, "cobol", "1.0"); !errors.Is(err, versions.ErrUnknownTool) {
		t.Errorf("check: err = %v, want ErrUnknownTool", err)
	}

	cat, err := catalog.Parse([]byte(`schema: 1
tools:
  fixed:
    name: Fixed
    version_scheme: major.minor
    matrix_key: fixed-version
    sources:
      - {type: static, lines: [{line: "1.0"}]}
    policy: {rule: all}
`))
	if err != nil {
		t.Fatal(err)
	}
	r = newResolver(t, u, func(o *versions.Options) { o.Catalog = cat })
	if _, err := r.Matrix(ctx, "fixed", versions.MatrixRequest{}); !errors.Is(err, versions.ErrUnsupportedSource) {
		t.Errorf("err = %v, want ErrUnsupportedSource", err)
	}

	bad := overlay(t, "schema: 1\ntools:\n  cobol:\n    policy:\n      floor: \"1.0\"\n")
	if _, err := versions.New(versions.Options{Overlay: bad}); err == nil {
		t.Error("New with an overlay naming an unknown tool: want an error")
	}
}

func TestDeterminism(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, func(o *versions.Options) { o.Fetch.Cache = nil })
	var out [][]byte
	for range 2 {
		rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{Constraint: ">=3.12"})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, validated(t, rep))
	}
	if !bytes.Equal(out[0], out[1]) {
		t.Errorf("two identical calls differ:\n%s\n%s", out[0], out[1])
	}
}

func TestReportSchemaRejects(t *testing.T) {
	sch, err := reportSchema()
	if err != nil {
		t.Fatal(err)
	}
	u := newUpstream(t)
	rep, err := newResolver(t, u, nil).Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	good := validated(t, rep)
	for name, edit := range map[string]func(map[string]any){
		"unknown property": func(m map[string]any) { m["extra"] = true },
		"unknown kind":     func(m map[string]any) { m["source"].(map[string]any)["kind"] = "network" },
		"bad date": func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["released"] = "2026-13-01"
		},
		"bad reason": func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["reason"] = "whim"
		},
		"bad time": func(m map[string]any) { m["generated_at"] = "2026-10-28 12:00" },
	} {
		var m map[string]any
		if err := json.Unmarshal(good, &m); err != nil {
			t.Fatal(err)
		}
		edit(m)
		if err := sch.Validate(m); err == nil {
			t.Errorf("%s: the schema accepted the report", name)
		}
	}
}
