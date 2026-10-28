// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	flagFormat = "--format"
	cmdMatrix  = "matrix"
	toolPython = "python"
)

func matrixOf(t *testing.T, m map[string]any) []string {
	t.Helper()
	mx, ok := m[cmdMatrix].(map[string]any)
	if !ok {
		t.Fatalf("report has no matrix: %v", m)
	}
	vs, ok := mx["versions"].([]any)
	if !ok {
		t.Fatalf("matrix has no versions: %v", mx)
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("matrix version %v is not a string", v)
		}
		out = append(out, s)
	}
	return out
}

func TestMatrixText(t *testing.T) {
	h := newHarness(t)
	r := h.run(cmdMatrix, toolPython)
	r.want(t, ExitOK)
	wantContains(t, "stdout", r.stdout,
		"tool:    python (Python)\n",
		"matrix:  3.11 3.12 3.13 3.14 3.15\n",
		"latest:  3.15\n",
		"source:  live\n",
		"LINE  STATUS    MATRIX  REASON       NEWEST   EOL\n",
		"3.10  eol       no      below-floor  3.10.22  2026-10-01\n",
		"3.13  security  yes     -            3.13.18  2029-10-31\n")
}

func TestMatrixJSON(t *testing.T) {
	h := newHarness(t)
	r := h.run(cmdMatrix, flagFormat, formatJSON, toolPython)
	r.want(t, ExitOK)
	m := validReport(t, []byte(r.stdout))
	if got := matrixOf(t, m); !slices.Equal(got, []string{"3.11", "3.12", "3.13", "3.14", "3.15"}) {
		t.Errorf("matrix = %v", got)
	}
}

func TestMatrixConstraint(t *testing.T) {
	h := newHarness(t)
	r := h.run(flagFormat, formatJSON, cmdMatrix, "--constraint", ">=3.12,<3.15", toolPython)
	r.want(t, ExitOK)
	if got := matrixOf(t, validReport(t, []byte(r.stdout))); !slices.Equal(got, []string{"3.12", "3.13", "3.14"}) {
		t.Errorf("matrix = %v, want 3.12 3.13 3.14", got)
	}
}

func TestMatrixStrict(t *testing.T) {
	h := newHarness(t)
	r := h.run(flagFormat, formatJSON, cmdMatrix, "--strict", "--constraint", "<3.0", toolPython)
	r.want(t, ExitOutOfRange)
	m := validReport(t, []byte(r.stdout))
	if got := matrixOf(t, m); len(got) != 5 {
		t.Errorf("matrix = %v, want the unconstrained matrix", got)
	}
	wantContains(t, "stderr", r.stderr, "constraint allows no line")
}

func TestMatrixFlagsExclusive(t *testing.T) {
	h := newHarness(t)
	r := h.run(cmdMatrix, "--constraint", ">=3.12", "--project", t.TempDir(), toolPython)
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "mutually exclusive", "help")
}

func TestMatrixProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"),
		[]byte("[project]\nrequires-python = \">=3.13\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	r := h.run(cmdMatrix, "--project", dir, toolPython)
	r.want(t, ExitOK)
	wantContains(t, "stdout", r.stdout, "matrix:      3.13 3.14 3.15\n",
		"constraint:  >=3.13 (applied, from pyproject.toml:project.requires-python)\n")
}

func TestPolicyEOLFail(t *testing.T) {
	h := newHarness(t)
	h.hooks.now = func() time.Time { return time.Date(2027, 11, 1, 12, 0, 0, 0, time.UTC) }
	path := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(path,
		[]byte("schema: 1\ntools:\n  python:\n    policy:\n      eol_behaviour: fail\n"), 0o600); err != nil { //nolint:misspell // field name fixed by the catalog format
		t.Fatal(err)
	}
	r := h.run("--policy", path, flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitEOL)
	m := validReport(t, []byte(r.stdout))
	if p, ok := m["policy"].(map[string]any); !ok || p["origin"] != path || p["overlay"] != true {
		t.Errorf("policy = %v, want the overlay and its origin", m["policy"])
	}
}

func TestPolicyMissing(t *testing.T) {
	h := newHarness(t)
	r := h.run("--policy", filepath.Join(t.TempDir(), "absent.yaml"), cmdMatrix, toolPython)
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "only local policy files are supported so far")
	r = h.run("--policy", "owner/repo//policy.yaml@0123abc", cmdMatrix, toolPython)
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "only local policy files are supported so far")
}

func TestOfflineSnapshot(t *testing.T) {
	h := newHarness(t)
	h.u.setFailAll(http.StatusInternalServerError)
	r := h.run("--offline", "--no-cache", flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	src, ok := validReport(t, []byte(r.stdout))["source"].(map[string]any)
	if !ok || src["kind"] != "snapshot" || src["snapshot_date"] != "2026-10-28" {
		t.Errorf("source = %v, want the snapshot of 2026-10-28", src)
	}

	snap := embeddedSnapshot(t)
	delete(snap.EndOfLife, toolPython)
	h.hooks.snapshot = snap
	r = h.run("--offline", "--no-cache", cmdMatrix, toolPython)
	r.want(t, ExitNoFacts)
	wantContains(t, "stderr", r.stderr, "no release facts available")
}

func TestCheck(t *testing.T) {
	h := newHarness(t)
	r := h.run("check", toolPython, "3.10.22")
	r.want(t, ExitOK)
	wantContains(t, "stdout", r.stdout, "status:           eol\n", "supported:        no\n")

	r = h.run(flagFormat, formatJSON, "check", "--fail", toolPython, "3.10.22")
	r.want(t, ExitNotSupported)
	m := validReport(t, []byte(r.stdout))
	if c, ok := m["check"].(map[string]any); !ok || c["status"] != "eol" || c["supported"] != false {
		t.Errorf("check = %v, want eol and unsupported", m["check"])
	}
	wantContains(t, "stderr", r.stderr, "python 3.10.22 is not supported")

	h.run("check", "--fail", toolPython, "3.13.4").want(t, ExitOK)

	r = h.run("check", toolPython, "stable")
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "not a version")
}

func TestUnknownTool(t *testing.T) {
	h := newHarness(t)
	r := h.run(cmdMatrix, "nope")
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, `unknown tool "nope"`)
	if r.stdout != "" {
		t.Errorf("stdout = %q, want nothing", r.stdout)
	}
}

func TestList(t *testing.T) {
	h := newHarness(t)
	r := h.run("list")
	r.want(t, ExitOK)
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") ||
		!strings.HasPrefix(lines[1], "go ") || !strings.HasPrefix(lines[2], "python ") {
		t.Errorf("list =\n%s", r.stdout)
	}
	wantContains(t, "list", r.stdout, "cpython,python3,py", "python-version", "endoflife,actions-manifest")

	r = h.run(flagFormat, formatJSON, "list")
	r.want(t, ExitOK)
	var rows []listRow
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "go" || rows[1].MatrixKey != "python-version" ||
		!slices.Equal(rows[1].Sources, []string{"endoflife", "actions-manifest"}) ||
		!slices.Equal(rows[0].Aliases, []string{"golang"}) {
		t.Errorf("rows = %+v", rows)
	}

	r = h.run("list", "go")
	r.want(t, ExitOK)
	wantContains(t, "list go", r.stdout, "tool:    go (Go)\n", "matrix:  1.26 1.27\n", "LINE  STATUS")
}

func TestFlagPlacement(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{flagFormat, formatJSON, cmdMatrix, toolPython},
		{cmdMatrix, flagFormat, formatJSON, toolPython},
	} {
		r := h.run(args...)
		r.want(t, ExitOK)
		validReport(t, []byte(r.stdout))
	}
	r := h.run(cmdMatrix, toolPython, flagFormat, formatJSON)
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "flags must come before arguments", "help")
}

func TestUsageErrors(t *testing.T) {
	h := newHarness(t)
	for name, args := range map[string][]string{
		"no command":      nil,
		"unknown command": {"frobnicate"},
		"unknown flag":    {cmdMatrix, "--frob", toolPython},
		"bad format":      {flagFormat, "yaml", cmdMatrix, toolPython},
		"no tool":         {cmdMatrix},
		"check one arg":   {"check", toolPython},
		"bad timeout":     {"--timeout", "0s", cmdMatrix, toolPython},
		"bad retries":     {"--retries", "-1", cmdMatrix, toolPython},
	} {
		r := h.run(args...)
		if r.code != ExitFailure || !strings.Contains(r.stderr, "help") {
			t.Errorf("%s: exit %d, stderr %q; want 1 and a hint", name, r.code, r.stderr)
		}
	}
}

func TestHelp(t *testing.T) {
	h := newHarness(t)
	r := h.runRaw("help")
	r.want(t, ExitOK)
	wantContains(t, "help", r.stdout, cmdMatrix, "check", "list", "-format")
	r = h.runRaw("help", cmdMatrix)
	r.want(t, ExitOK)
	wantContains(t, "help matrix", r.stdout, "-constraint", "-strict", "-offline")
	h.runRaw("check", "-h").want(t, ExitOK)
	h.runRaw("help", "nope").want(t, ExitFailure)
}

func TestVerbose(t *testing.T) {
	h := newHarness(t)
	r := h.run("--verbose", cmdMatrix, "go")
	r.want(t, ExitOK)
	wantContains(t, "stderr", r.stderr, "source: endoflife go: live", "source: actions-manifest actions/go-versions: live")
}
