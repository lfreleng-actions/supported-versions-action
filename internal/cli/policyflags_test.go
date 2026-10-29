// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	flagEOLBehaviour = "--eol-behaviour" //nolint:misspell // spelled as the catalog field
	flagMatrixKey    = "--matrix-key"
	flagPolicy       = "--policy"
	flagPrerelease   = "--prerelease"
)

// writeOverlay writes an overlay adjusting the python policy and
// returns its path.
func writeOverlay(t *testing.T, policy string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(path, []byte("schema: 1\ntools:\n  python:\n    policy:\n"+policy), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func policyOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	p, ok := m["policy"].(map[string]any)
	if !ok {
		t.Fatalf("report has no policy: %v", m)
	}
	return p
}

// lineReasons maps each line of a report to its reason.
func lineReasons(t *testing.T, m map[string]any) map[string]string {
	t.Helper()
	lines, ok := m["lines"].([]any)
	if !ok {
		t.Fatalf("report has no lines: %v", m)
	}
	out := map[string]string{}
	for _, l := range lines {
		lm, ok := l.(map[string]any)
		if !ok {
			t.Fatalf("line %v is not an object", l)
		}
		line, _ := lm["line"].(string)     //nolint:errcheck // a missing line shows as ""
		reason, _ := lm["reason"].(string) //nolint:errcheck // an included line has no reason
		out[line] = reason
	}
	return out
}

func TestEOLBehaviourFlag(t *testing.T) {
	h := newHarness(t)
	path := writeOverlay(t, "      floor: \"3.9\"\n")

	r := h.run(flagPolicy, path, flagEOLBehaviour, "strip", flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	m := validReport(t, []byte(r.stdout))
	reasons := lineReasons(t, m)
	for _, l := range []string{"3.9", "3.10"} {
		if reasons[l] != "eol-stripped" {
			t.Errorf("line %s reason = %q, want eol-stripped", l, reasons[l])
		}
	}
	for _, v := range matrixOf(t, m) {
		if v == "3.9" || v == "3.10" {
			t.Errorf("matrix = %v, want the EOL lines stripped", matrixOf(t, m))
		}
	}
	if p := policyOf(t, m); p["eol_behaviour"] != "strip" { //nolint:misspell // field name fixed by the catalog format
		t.Errorf("policy = %v, want strip", p)
	}

	h.run(flagPolicy, path, flagEOLBehaviour, "fail", cmdMatrix, toolPython).want(t, ExitEOL)
	// The flag reaches check and list <tool> as well. Check never
	// fails on end-of-life lines, so look at its policy instead.
	r = h.run(flagPolicy, path, flagEOLBehaviour, "fail", flagFormat, formatJSON, "check", toolPython, "3.9")
	r.want(t, ExitOK)
	if p := policyOf(t, validReport(t, []byte(r.stdout))); p["eol_behaviour"] != "fail" { //nolint:misspell // field name fixed by the catalog format
		t.Errorf("check policy = %v, want fail", p)
	}
	h.run(flagPolicy, path, "list", flagEOLBehaviour, "fail", toolPython).want(t, ExitEOL)

	r = h.run(flagEOLBehaviour, "bogus", cmdMatrix, toolPython)
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "want warn, strip, fail or allow", "help")
}

func TestPrereleaseFlag(t *testing.T) {
	h := newHarness(t)
	path := writeOverlay(t, "      prerelease: true\n")

	r := h.run(flagPolicy, path, flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	if p := policyOf(t, validReport(t, []byte(r.stdout))); p["prerelease"] != true {
		t.Errorf("policy = %v, want the overlay's prerelease", p)
	}

	r = h.run(flagPolicy, path, flagPrerelease+"=false", flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	if p := policyOf(t, validReport(t, []byte(r.stdout))); p["prerelease"] != false {
		t.Errorf("policy = %v, want prerelease overridden to false", p)
	}

	r = h.run(flagPrerelease, flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	if p := policyOf(t, validReport(t, []byte(r.stdout))); p["prerelease"] != true {
		t.Errorf("policy = %v, want prerelease set by the bare flag", p)
	}

	h.run(flagPrerelease+"=maybe", cmdMatrix, toolPython).want(t, ExitFailure)
}

func TestMatrixKeyFlag(t *testing.T) {
	h := newHarness(t)
	r := h.run(flagMatrixKey, toolPython, flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	m := validReport(t, []byte(r.stdout))
	if mx, ok := m[cmdMatrix].(map[string]any); !ok || mx["key"] != toolPython {
		t.Errorf("matrix = %v, want the key python", m[cmdMatrix])
	}

	r = h.run(flagMatrixKey, "go_ver", flagFormat, formatJSON, "check", "go", "1.26")
	r.want(t, ExitOK)
	if mx, ok := validReport(t, []byte(r.stdout))[cmdMatrix].(map[string]any); !ok || mx["key"] != "go_ver" {
		t.Errorf("check matrix = %v, want the key go_ver", mx)
	}

	r = h.run(flagMatrixKey, "py", "list", toolPython)
	r.want(t, ExitOK)
	wantContains(t, "list", r.stdout, "tool:    python (Python)\n")

	outPath, _ := githubEnv(t, h)
	h.run(flagMatrixKey, toolPython, formatGitHubFlag, cmdMatrix, toolPython).want(t, ExitOK)
	var mj map[string][]string
	if err := json.Unmarshal([]byte(parseOutputs(t, readFile(t, outPath))["matrix_json"]), &mj); err != nil {
		t.Fatal(err)
	}
	if _, ok := mj[toolPython]; !ok || len(mj) != 1 {
		t.Errorf("matrix_json = %v, want the key python alone", mj)
	}

	for _, key := range []string{"1x", "a b", "a" + strings.Repeat("b", 64), "-x"} {
		r = h.run(flagMatrixKey, key, cmdMatrix, toolPython)
		if r.code != ExitFailure || !strings.Contains(r.stderr, "--matrix-key") {
			t.Errorf("--matrix-key %q: exit %d, stderr %q; want a usage error", key, r.code, r.stderr)
		}
	}
	h.run(flagMatrixKey, "a"+strings.Repeat("b", 63), cmdMatrix, toolPython).want(t, ExitOK)
}

func TestPolicyFlagsOrigin(t *testing.T) {
	h := newHarness(t)
	path := writeOverlay(t, "      floor: \"3.12\"\n")
	for name, tc := range map[string]struct {
		args   []string
		origin string
	}{
		"flags":   {[]string{flagEOLBehaviour, "allow"}, originFlags},
		"overlay": {[]string{flagPolicy, path}, path},
		"both":    {[]string{flagPolicy, path, flagPrerelease + "=false"}, path + "; " + originFlags},
	} {
		args := append(append([]string{}, tc.args...), flagFormat, formatJSON, cmdMatrix, toolPython)
		r := h.run(args...)
		r.want(t, ExitOK)
		p := policyOf(t, validReport(t, []byte(r.stdout)))
		if p["overlay"] != true || p["origin"] != tc.origin {
			t.Errorf("%s: policy = %v, want overlay true and origin %q", name, p, tc.origin)
		}
	}

	r := h.run(flagFormat, formatJSON, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	if p := policyOf(t, validReport(t, []byte(r.stdout))); p["overlay"] != false || p["origin"] != "" {
		t.Errorf("policy = %v, want no overlay", p)
	}
}
