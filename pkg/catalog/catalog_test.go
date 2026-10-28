// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package catalog_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
)

const python = "python"

func TestEmbedded(t *testing.T) {
	c, err := catalog.Embedded()
	if err != nil {
		t.Fatalf("Embedded: %v", err)
	}
	if got, want := c.IDs(), []string{"go", python}; !reflect.DeepEqual(got, want) {
		t.Errorf("IDs() = %v, want %v", got, want)
	}
	for name, want := range map[string]string{
		"py":      python,
		"GoLang":  "go",
		"PYTHON":  python,
		"cpython": python,
		"go":      "go",
	} {
		id, tool, ok := c.Lookup(name)
		if !ok || id != want || tool.Name == "" {
			t.Errorf("Lookup(%q) = %q, %q, %v; want %q", name, id, tool.Name, ok, want)
		}
	}
	if _, _, ok := c.Lookup("cobol"); ok {
		t.Error(`Lookup("cobol") found a tool`)
	}

	py := c.Tools[python].Policy
	if py.Floor != "3.11" || py.Rule != catalog.RuleAll || py.WarnDaysBeforeEOL != 90 {
		t.Errorf("python policy = %+v", py)
	}
	goPolicy := c.Tools["go"].Policy
	if goPolicy.EOLBehaviour != catalog.DefaultEOLBehaviour ||
		goPolicy.WarnDaysBeforeEOL != catalog.DefaultWarnDaysBeforeEOL ||
		goPolicy.Admit != catalog.AdmitInstallable {
		t.Errorf("go policy defaults not applied: %+v", goPolicy)
	}
	detect := c.Tools[python].Constraints.Detect
	if len(detect) != 6 || detect[5].File != ".python-version" {
		t.Errorf("python detect rules = %+v", detect)
	}
}

func TestLoadValid(t *testing.T) {
	c := loadValid(t)
	want := []string{"gradle", "java", "legacy", "maven", "node", python, "rust"}
	if got := c.IDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs() = %v, want %v", got, want)
	}
	types := map[string]bool{}
	for _, tool := range c.Tools {
		for _, s := range tool.Sources {
			types[s.Type] = true
		}
	}
	for _, typ := range []string{
		"endoflife", "actions-manifest", "adoptium", "gradle-services",
		"maven-metadata", "rust-channel", "github-releases", "static",
	} {
		if !types[typ] {
			t.Errorf("source type %q not covered by the valid fixture", typ)
		}
	}
	if r := c.Tools["maven"].Compatibility["java"]["4.0"]; r.Min != "17" || r.Max != "25" {
		t.Errorf("maven/java compatibility for 4.0 = %+v", r)
	}
	lines := c.Tools["legacy"].Sources[0].Lines
	if lines[0].Released != "2020-01-15" || lines[1].Line != "1.10" || !lines[1].LTS {
		t.Errorf("static lines = %+v", lines)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := catalog.Load(filepath.Join("testdata", "catalog", "absent.yaml")); err == nil {
		t.Fatal("Load of a missing file succeeded")
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	for name, data := range map[string]string{
		"empty":         "",
		"syntax":        "schema: [1",
		"duplicate key": "schema: 1\nschema: 1\ntools: {}\n",
		"not a mapping": "- 1\n",
	} {
		if _, err := catalog.Parse([]byte(data)); err == nil {
			t.Errorf("%s: Parse succeeded", name)
		}
	}
}

// TestParseReportsEveryProblem checks validation does not stop at the
// first problem.
func TestParseReportsEveryProblem(t *testing.T) {
	data := `schema: 1
tools:
  demo:
    name: Demo
    version_scheme: major.minor
    matrix_key: demo-version
    sources: [{type: endoflife, product: demo}]
    policy: {rule: latest, floor: "3.x", ceiling: "4", current: true}
`
	_, err := catalog.Parse([]byte(data))
	got := errorPaths(err)
	want := []string{
		"tools.demo.policy.ceiling",
		"tools.demo.policy.count",
		"tools.demo.policy.current",
		"tools.demo.policy.floor",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("error paths = %v, want %v (error: %v)", got, want, err)
	}
	if err != nil && !strings.Contains(err.Error(), `tools.demo.policy.floor: "3.x" is not a major.minor line`) {
		t.Errorf("error does not describe the floor: %v", err)
	}
}

func TestParseRejectsRecursiveAliases(t *testing.T) {
	for name, data := range map[string]string{
		"sequence": "schema: 1\ntools: &loop [*loop]\n",
		"mapping":  "schema: 1\ntools: &loop {demo: *loop}\n",
		"nested":   "schema: 1\ntools: &outer {demo: {inner: [*outer]}}\n",
	} {
		if _, err := catalog.Parse([]byte(data)); err == nil {
			t.Errorf("%s: Parse succeeded", name)
		}
		if _, err := catalog.ParseOverlay([]byte(data)); err == nil {
			t.Errorf("%s: ParseOverlay succeeded", name)
		}
	}
}

func TestParseBoundsAliasExpansion(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("schema: 1\nl0: &l0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&sb, "l%d: &l%d [", i, i)
		for j := range 10 {
			if j > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "*l%d", i-1)
		}
		sb.WriteString("]\n")
	}
	sb.WriteString("tools: {}\n")
	_, err := catalog.Parse([]byte(sb.String()))
	if err == nil || !strings.Contains(err.Error(), "expands to more than") {
		t.Fatalf("Parse = %v; want an expansion limit error", err)
	}
}

func TestParseFollowsAliases(t *testing.T) {
	data := `schema: 1
tools:
  one:
    name: One
    version_scheme: major
    matrix_key: one-version
    sources: [{type: adoptium}]
    policy: &policy {rule: all, floor: "8"}
  two:
    name: Two
    version_scheme: major
    matrix_key: two-version
    sources: [{type: adoptium}]
    policy: *policy
`
	c, err := catalog.Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Tools["two"].Policy.Floor != "8" {
		t.Errorf("aliased policy not applied: %+v", c.Tools["two"].Policy)
	}
}

// TestSchemaErrorPathsPerObject checks the same invalid property name
// in two objects is reported at both paths, not twice at the first.
func TestSchemaErrorPathsPerObject(t *testing.T) {
	data := `schema: 1
tools:
  a: &tool
    name: A
    version_scheme: major
    matrix_key: a-version
    sources: [{type: adoptium}]
    policy: {rule: all}
    compatibility: {BAD: {"1": {min: "1"}}}
  b: *tool
`
	_, err := catalog.Parse([]byte(data))
	got := errorPaths(err)
	want := []string{"tools.a.compatibility.BAD", "tools.b.compatibility.BAD"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("error paths = %v, want %v (error: %v)", got, want, err)
	}
}

// TestLargeLinesCompareExactly checks bounds too large for an int are
// still ordered correctly rather than saturating to equal values.
func TestLargeLinesCompareExactly(t *testing.T) {
	const tmpl = `schema: 1
tools:
  a:
    name: A
    version_scheme: major
    matrix_key: a-version
    sources: [{type: adoptium}]
    policy: {rule: all, floor: "%s", ceiling: "%s"}
    compatibility: {b: {"1": {min: "%s", max: "%s"}}}
  b:
    name: B
    version_scheme: major.minor
    matrix_key: b-version
    sources: [{type: adoptium}]
    policy: {rule: all}
`
	const (
		small = "9223372036854775808"
		big   = "9223372036854775809"
	)
	_, err := catalog.Parse(fmt.Appendf(nil, tmpl, big, small, "1."+big, "1."+small))
	got := errorPaths(err)
	want := []string{"tools.a.compatibility.b.1.min", "tools.a.policy.floor"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("error paths = %v, want %v (error: %v)", got, want, err)
	}
	if _, err := catalog.Parse(fmt.Appendf(nil, tmpl, small, big, "1."+small, "1."+big)); err != nil {
		t.Errorf("ordered large bounds rejected: %v", err)
	}
}

func TestOverlayFloor(t *testing.T) {
	base := loadValid(t)
	o := loadOverlay(t, "valid-floor.yaml")
	got, err := base.Apply(o)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	p := got.Tools[python].Policy
	if p.Floor != "3.12" {
		t.Errorf("floor = %q, want 3.12", p.Floor)
	}
	if p.Ceiling != "3.14" || p.Admit != catalog.AdmitExplicit || p.Rule != catalog.RuleAll {
		t.Errorf("fields the overlay omits changed: %+v", p)
	}
}

func TestOverlayZeroValuesOverride(t *testing.T) {
	base := loadValid(t)
	if !base.Tools["node"].Policy.Current || base.Tools["java"].Policy.GraceDays != 30 {
		t.Fatal("fixture no longer sets node current: true and java grace_days: 30")
	}
	got, err := base.Apply(loadOverlay(t, "valid-zero-values.yaml"))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.Tools["node"].Policy.Current {
		t.Error("current: false did not override true")
	}
	if got.Tools["java"].Policy.GraceDays != 0 {
		t.Error("grace_days: 0 did not override 30")
	}
	if got.Tools["java"].Policy.EOLBehaviour != "strip" {
		t.Error("an omitted field lost its catalog value")
	}
}

func TestApplyDoesNotMutate(t *testing.T) {
	base := loadValid(t)
	before := loadValid(t)
	overlay := &catalog.Overlay{Schema: 1, Tools: map[string]catalog.ToolOverlay{
		python: {Policy: catalog.PolicyOverride{Floor: ptr("3.12")}},
		"rust": {Policy: catalog.PolicyOverride{Labels: []string{"beta"}}},
	}}
	got, err := base.Apply(overlay)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(base, before) {
		t.Error("Apply modified its receiver")
	}

	// The result must not share mutable state with the input.
	got.Tools["maven"].Compatibility["java"]["3.9"] = catalog.LineRange{Min: "11"}
	got.Tools["legacy"].Sources[0].Lines[0].Line = "9.9"
	got.Tools[python].Constraints.Detect[0].File = "changed"
	got.Tools["rust"].Policy.Labels[0] = "changed"
	if !reflect.DeepEqual(base, before) {
		t.Error("the result of Apply shares state with its receiver")
	}

	same, err := base.Apply(nil)
	if err != nil || !reflect.DeepEqual(same, before) {
		t.Errorf("Apply(nil) = %v, %v; want an equal copy", same, err)
	}
}

// TestRejectedOverlays applies every testdata/overlay/reject-*.yaml
// fixture, each valid on its own, to the embedded catalog and checks
// Apply fails at the path named by its comment.
func TestRejectedOverlays(t *testing.T) {
	base, err := catalog.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join("testdata", "overlay", "reject-*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no reject fixtures: %v", err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, want := readFixture(t, file, "# Expect Apply to fail at: ")
			o, err := catalog.ParseOverlay(data)
			if err != nil {
				t.Fatalf("ParseOverlay: %v", err)
			}
			_, err = base.Apply(o)
			assertErrorAt(t, err, want)
		})
	}
}

// TestApplyChecksOverlaySchema checks an overlay built in code, which
// ParseOverlay never saw, meets the same rules as a parsed one.
func TestApplyChecksOverlaySchema(t *testing.T) {
	base, err := catalog.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		overlay *catalog.Overlay
		want    string
	}{
		"schema version": {
			&catalog.Overlay{Schema: 2, Tools: map[string]catalog.ToolOverlay{}},
			"schema",
		},
		"rule": {
			&catalog.Overlay{Schema: 1, Tools: map[string]catalog.ToolOverlay{
				python: {Policy: catalog.PolicyOverride{Rule: ptr("newest")}},
			}},
			"tools.python.policy.rule",
		},
		"grace days": {
			&catalog.Overlay{Schema: 1, Tools: map[string]catalog.ToolOverlay{
				python: {Policy: catalog.PolicyOverride{GraceDays: ptr(-1)}},
			}},
			"tools.python.policy.grace_days",
		},
		"labels": {
			&catalog.Overlay{Schema: 1, Tools: map[string]catalog.ToolOverlay{
				python: {Policy: catalog.PolicyOverride{Labels: []string{""}}},
			}},
			"tools.python.policy.labels[0]",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := base.Apply(tc.overlay)
			assertErrorAt(t, err, tc.want)
		})
	}
}

// TestApplyChecksCatalogSchema checks Apply validates the merged
// catalog against the schema, so a catalog built in code with an
// invalid field is rejected rather than passed on.
func TestApplyChecksCatalogSchema(t *testing.T) {
	base, err := catalog.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	tool := base.Tools["go"]
	tool.Policy.Admit = "never"
	base.Tools["go"] = tool
	_, err = base.Apply(&catalog.Overlay{Schema: 1, Tools: map[string]catalog.ToolOverlay{}})
	assertErrorAt(t, err, "tools.go.policy.admit")
}

func TestLoadOverlay(t *testing.T) {
	if _, err := catalog.LoadOverlay(filepath.Join("testdata", "overlay", "absent.yaml")); err == nil {
		t.Error("LoadOverlay of a missing file succeeded")
	}
}

func loadValid(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load(filepath.Join("testdata", "catalog", "valid.yaml"))
	if err != nil {
		t.Fatalf("Load valid.yaml: %v", err)
	}
	return c
}

func loadOverlay(t *testing.T, name string) *catalog.Overlay {
	t.Helper()
	o, err := catalog.LoadOverlay(filepath.Join("testdata", "overlay", name))
	if err != nil {
		t.Fatalf("LoadOverlay %s: %v", name, err)
	}
	return o
}

// readFixture returns a fixture's bytes and the path its expectation
// comment names.
func readFixture(t *testing.T, file, marker string) ([]byte, string) {
	t.Helper()
	data, err := os.ReadFile(file) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if want, ok := strings.CutPrefix(line, marker); ok {
			return data, strings.TrimSpace(want)
		}
	}
	t.Fatalf("%s has no %q comment", file, marker)
	return nil, ""
}

func assertErrorAt(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error; want one at %s", want)
	}
	if paths := errorPaths(err); !slices.Contains(paths, want) {
		t.Fatalf("error paths %v do not include %s; error: %v", paths, want, err)
	}
}

// errorPaths returns the sorted, de-duplicated paths of every
// FieldError joined into err.
func errorPaths(err error) []string {
	var paths []string
	var walk func(error)
	walk = func(e error) {
		if fe, ok := e.(*catalog.FieldError); ok {
			paths = append(paths, fe.Path)
			return
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range j.Unwrap() {
				walk(inner)
			}
		}
	}
	if err != nil {
		walk(err)
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

func ptr[T any](v T) *T { return &v }
