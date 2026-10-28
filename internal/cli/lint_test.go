// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	subLint = "lint"
	okLine  = "ok\n"
)

func writeTemp(t *testing.T, name, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCatalogLintEmbedded(t *testing.T) {
	h := newHarness(t)
	r := h.runRaw(cmdCatalog, subLint)
	r.want(t, ExitOK)
	if r.stdout != okLine {
		t.Errorf("stdout = %q, want ok", r.stdout)
	}
}

func TestCatalogLintFile(t *testing.T) {
	h := newHarness(t)
	broken := writeTemp(t, "catalog.yaml", `schema: 1
tools:
  demo:
    version_scheme: major.minor
    matrix_key: demo-version
    sources:
      - {type: endoflife, product: demo}
    policy:
      rule: all
  other:
    name: Other
    version_scheme: major.minor
    sources:
      - {type: endoflife, product: other}
    policy:
      rule: all
`)
	r := h.runRaw(cmdCatalog, subLint, broken)
	r.want(t, ExitFailure)
	wantContains(t, "stdout", r.stdout, "tools.demo.name", "tools.other")
	if n := strings.Count(r.stdout, "\n"); n < 2 {
		t.Errorf("stdout has %d lines, want one per problem:\n%s", n, r.stdout)
	}

	good := writeTemp(t, "catalog.yaml", `schema: 1
tools:
  demo:
    name: Demo
    version_scheme: major.minor
    matrix_key: demo-version
    sources:
      - {type: endoflife, product: demo}
    policy:
      rule: all
`)
	r = h.runRaw(cmdCatalog, subLint, good)
	r.want(t, ExitOK)
	if r.stdout != okLine {
		t.Errorf("stdout = %q, want ok", r.stdout)
	}
}

func TestCatalogLintOverlay(t *testing.T) {
	h := newHarness(t)
	for name, text := range map[string]string{
		"invalid":      "schema: 1\ntools:\n  python:\n    policy:\n      rule: sometimes\n",
		"unknown tool": "schema: 1\ntools:\n  cobol:\n    policy: {floor: \"85\"}\n",
	} {
		r := h.runRaw(cmdCatalog, subLint, "--overlay", writeTemp(t, "overlay.yaml", text))
		if r.code != ExitFailure || r.stdout == okLine || r.stdout == "" {
			t.Errorf("%s: exit %d, stdout %q; want 1 and the problems", name, r.code, r.stdout)
		}
	}
	r := h.runRaw(cmdCatalog, subLint, "--overlay",
		writeTemp(t, "overlay.yaml", "schema: 1\ntools:\n  python:\n    policy:\n      floor: \"3.12\"\n"))
	r.want(t, ExitOK)
}

func TestCatalogUsage(t *testing.T) {
	h := newHarness(t)
	h.runRaw(cmdCatalog).want(t, ExitFailure)
	h.runRaw(cmdCatalog, "fix").want(t, ExitFailure)
	h.runRaw(cmdCatalog, subLint, "--overlay").want(t, ExitFailure)
	h.runRaw(cmdCatalog, subLint, "a.yaml", "b.yaml").want(t, ExitFailure)
	r := h.runRaw("help", cmdCatalog)
	r.want(t, ExitOK)
	wantContains(t, "help catalog", r.stdout, "catalog lint [--overlay] [FILE]", "-overlay")
}

func TestVersion(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{"version"}, {"--version"}} {
		r := h.runRaw(args...)
		r.want(t, ExitOK)
		if !strings.HasPrefix(r.stdout, progName+" ") || len(strings.TrimSpace(r.stdout)) <= len(progName) {
			t.Errorf("%v printed %q", args, r.stdout)
		}
	}
	h.runRaw("version", "extra").want(t, ExitFailure)
}

func TestCatalogLintGitHub(t *testing.T) {
	h := newHarness(t)
	githubEnv(t, h)
	overlay := writeTemp(t, "overlay.yaml",
		"schema: 1\ntools:\n  python:\n    policy:\n      \"\\n::error::injected\\n\": 1\n      rule: sometimes\n")
	r := h.runRaw(formatGitHubFlag, cmdCatalog, subLint, "--overlay", overlay)
	r.want(t, ExitFailure)
	wantContains(t, "stdout", r.stdout, "%0A::error::injected%0A")
	wantNoInjection(t, "stdout", r.stdout)
	wantNoInjection(t, "stderr", r.stderr)
	for line := range strings.SplitSeq(strings.TrimSuffix(r.stdout, "\n"), "\n") {
		if !strings.HasPrefix(line, "::error::") {
			t.Errorf("stdout line %q is not an error annotation:\n%s", line, r.stdout)
		}
	}
	if n := strings.Count(r.stdout, "\n"); n < 3 {
		t.Errorf("stdout has %d annotations, want one per problem and the final error:\n%s", n, r.stdout)
	}
}

func TestCatalogFlagPlacement(t *testing.T) {
	h := newHarness(t)
	githubEnv(t, h)
	overlay := writeTemp(t, "overlay.yaml", "schema: 1\ntools:\n  python:\n    policy:\n      floor: \"3.12\"\n")
	for _, args := range [][]string{
		{formatGitHubFlag, cmdCatalog, subLint},
		{cmdCatalog, formatGitHubFlag, subLint},
		{cmdCatalog, subLint, formatGitHubFlag},
		{cmdCatalog, subLint, formatGitHubFlag, "--overlay", overlay},
		{cmdCatalog, formatGitHubFlag, subLint, "--overlay", overlay},
	} {
		r := h.runRaw(args...)
		if r.code != ExitOK || r.stdout != okLine {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want 0 and ok", args, r.code, r.stdout, r.stderr)
		}
	}
	r := h.runRaw(cmdCatalog, subLint, overlay, formatGitHubFlag)
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "flags must come before arguments")
	h.runRaw(cmdCatalog, "--overlay", subLint, overlay).want(t, ExitFailure)
}
