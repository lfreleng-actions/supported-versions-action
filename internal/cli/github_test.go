// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"bytes"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	formatGitHubFlag = "--format=github"
	outLatest        = "latest"
)

// parseOutputs reads a GITHUB_OUTPUT file as the runner does.
func parseOutputs(t *testing.T, data string) map[string]string {
	t.Helper()
	out := map[string]string{}
	lines := strings.Split(data, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" {
			continue
		}
		name, delim, ok := strings.Cut(line, "<<")
		if !ok {
			k, v, found := strings.Cut(line, "=")
			if !found {
				t.Fatalf("output line %q is neither form", line)
			}
			out[k] = v
			continue
		}
		var value []string
		for i++; i < len(lines) && lines[i] != delim; i++ {
			value = append(value, lines[i])
		}
		if i == len(lines) {
			t.Fatalf("output %q is not terminated", name)
		}
		if _, dup := out[name]; dup {
			t.Fatalf("output %q set twice", name)
		}
		out[name] = strings.Join(value, "\n")
	}
	return out
}

// githubEnv points the harness at fresh GITHUB_OUTPUT,
// GITHUB_STEP_SUMMARY and RUNNER_TEMP paths and returns the first two.
func githubEnv(t *testing.T, h *harness) (outputPath, summaryPath string) {
	t.Helper()
	dir := t.TempDir()
	outputPath = filepath.Join(dir, "output")
	summaryPath = filepath.Join(dir, "summary")
	h.vars["GITHUB_OUTPUT"] = outputPath
	h.vars["GITHUB_STEP_SUMMARY"] = summaryPath
	h.vars["RUNNER_TEMP"] = filepath.Join(dir, "runner-temp")
	return outputPath, summaryPath
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestGitHubMatrix(t *testing.T) {
	h := newHarness(t)
	outPath, sumPath := githubEnv(t, h)
	r := h.run(formatGitHubFlag, cmdMatrix, toolPython)
	r.want(t, ExitOK)
	outs := parseOutputs(t, readFile(t, outPath))
	keys := slices.Sorted(maps.Keys(outs))
	want := []string{"eol_versions", outLatest, "matrix_json", "report_path", "supported_versions", "versions_source"}
	if !slices.Equal(keys, want) {
		t.Errorf("output keys = %v, want %v", keys, want)
	}
	if got := outs["matrix_json"]; got != `{"python-version":["3.11","3.12","3.13","3.14","3.15"]}` {
		t.Errorf("matrix_json = %s", got)
	}
	if outs["supported_versions"] != "3.11 3.12 3.13 3.14 3.15" || outs[outLatest] != pythonMatrix[4] ||
		outs["eol_versions"] != "" || outs["versions_source"] != "live" {
		t.Errorf("outputs = %v", outs)
	}

	path := outs["report_path"]
	if !strings.HasPrefix(filepath.Base(path), "report-python-") || !strings.HasSuffix(path, ".json") {
		t.Errorf("report_path = %q", path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits to check.
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("report mode = %v, want 0600", fi.Mode().Perm())
		}
		if di.Mode().Perm() != 0o700 {
			t.Errorf("report directory mode = %v, want 0700", di.Mode().Perm())
		}
	}
	validReport(t, []byte(readFile(t, path)))

	wantContains(t, "summary", readFile(t, sumPath),
		"## Python (`python`)\n",
		"- **Matrix** (`python-version`): `3.11` `3.12` `3.13` `3.14` `3.15`\n",
		"- **Source:** live\n",
		"| Line | Status | In matrix | Reason | Newest | EOL |\n",
		"| 3.13 | security | yes | - | 3.13.18 | 2029-10-31 |\n")
	wantContains(t, "stdout", r.stdout, "matrix:  3.11 3.12 3.13 3.14 3.15\n")
}

func TestGitHubCheck(t *testing.T) {
	h := newHarness(t)
	outPath, _ := githubEnv(t, h)
	h.run(formatGitHubFlag, "check", toolPython, "3.10.22").want(t, ExitOK)
	outs := parseOutputs(t, readFile(t, outPath))
	for k, v := range map[string]string{
		"status":          "eol",
		"supported":       "false",
		"policy_excluded": "false",
		"eol_date":        "2026-10-01",
	} {
		if got, ok := outs[k]; !ok || got != v {
			t.Errorf("%s = %q (set %v), want %q", k, got, ok, v)
		}
	}
	for _, k := range []string{"matrix_json", "supported_versions", outLatest, "eol_versions", "versions_source", "report_path"} {
		if _, ok := outs[k]; !ok {
			t.Errorf("output %s missing", k)
		}
	}
}

func TestGitHubOfflineSource(t *testing.T) {
	h := newHarness(t)
	h.u.setFailAll(http.StatusInternalServerError)
	outPath, _ := githubEnv(t, h)
	delete(h.vars, "RUNNER_TEMP")
	h.run("--offline", "--no-cache", formatGitHubFlag, cmdMatrix, toolPython).want(t, ExitOK)
	outs := parseOutputs(t, readFile(t, outPath))
	if outs["versions_source"] != "snapshot:2026-10-28" {
		t.Errorf("versions_source = %q, want snapshot:2026-10-28", outs["versions_source"])
	}
	if p, ok := outs["report_path"]; !ok || p != "" {
		t.Errorf("report_path = %q (set %v), want empty without RUNNER_TEMP", p, ok)
	}
}

func TestGitHubNeedsOutput(t *testing.T) {
	h := newHarness(t)
	r := h.run(formatGitHubFlag, cmdMatrix, toolPython)
	r.want(t, ExitFailure)
	wantContains(t, "stderr", r.stderr, "--format github must run in GitHub Actions")
}

func TestGitHubError(t *testing.T) {
	h := newHarness(t)
	githubEnv(t, h)
	r := h.run(formatGitHubFlag, cmdMatrix, "nope")
	r.want(t, ExitFailure)
	wantContains(t, "stdout", r.stdout, `::error::unknown tool "nope"`)
}

func TestOutputInjection(t *testing.T) {
	evil := "3.12\nghadelim_AAAA\nlatest<<EOF\n9.99\nEOF\nDELIM"
	data := encodeOutputs([]output{{"first", evil}, {"second", "9.0"}})
	outs := parseOutputs(t, string(data))
	if len(outs) != 2 || outs["first"] != evil || outs["second"] != "9.0" {
		t.Errorf("outputs = %q", outs)
	}
}

func TestWarningAnnotation(t *testing.T) {
	h := newHarness(t)
	outPath, _ := githubEnv(t, h)
	var stdout bytes.Buffer
	e := &env{stdout: &stdout, stderr: &bytes.Buffer{}, getenv: func(k string) string { return h.vars[k] }}
	rep := versions.Report{
		Tool:     toolPython,
		Name:     "Python",
		Source:   versions.SourceSummary{Kind: versions.KindLive},
		Matrix:   versions.Matrix{Key: "python-version", Versions: []string{"3.0"}},
		Warnings: []string{"100% sure\r\n::error::injected"},
	}
	if err := e.emitGitHub(rep); err != nil {
		t.Fatal(err)
	}
	wantContains(t, "stdout", stdout.String(), "::warning::100%25 sure%0D%0A::error::injected\n")
	if strings.Contains(stdout.String(), "\n::error::") {
		t.Errorf("a warning injected an annotation:\n%s", stdout.String())
	}
	if outs := parseOutputs(t, readFile(t, outPath)); outs["matrix_json"] != `{"python-version":["3.0"]}` {
		t.Errorf("matrix_json = %q", outs["matrix_json"])
	}
}

func TestGitHubEOLFail(t *testing.T) {
	h := newHarness(t)
	h.hooks.now = func() time.Time { return time.Date(2027, 11, 1, 12, 0, 0, 0, time.UTC) }
	outPath, _ := githubEnv(t, h)
	policyPath := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(policyPath,
		[]byte("schema: 1\ntools:\n  python:\n    policy:\n      eol_behaviour: fail\n"), 0o600); err != nil { //nolint:misspell // field name fixed by the catalog format
		t.Fatal(err)
	}
	r := h.run("--policy", policyPath, formatGitHubFlag, cmdMatrix, toolPython)
	r.want(t, ExitEOL)
	if outs := parseOutputs(t, readFile(t, outPath)); outs["eol_versions"] != "3.11" {
		t.Errorf("eol_versions = %q, want 3.11", outs["eol_versions"])
	}
	wantContains(t, "stdout", r.stdout, "::error::")
}

// TestGitHubLabelEqualsLineRejected checks an overlay label equal to
// a release line is rejected before it can reach the matrix.
func TestGitHubLabelEqualsLineRejected(t *testing.T) {
	h := newHarness(t)
	outPath, _ := githubEnv(t, h)
	policyPath := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(policyPath,
		[]byte("schema: 1\ntools:\n  python:\n    policy:\n      labels: [\"3.12\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("--policy", policyPath, formatGitHubFlag, cmdMatrix, toolPython)
	r.want(t, ExitFailure)
	wantContains(t, "stdout", r.stdout, "::error::tools.python.policy.labels[0]: ")
	if _, err := os.Stat(outPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("GITHUB_OUTPUT written: %v", err)
	}
}

// TestGitHubLabelEqualsLine checks that a label equal to a release
// line, in a report the catalog checks never saw, is still dropped
// from supported_versions by its position after the lines, and the
// line itself kept.
func TestGitHubLabelEqualsLine(t *testing.T) {
	const line = "1.1"
	rep := versions.Report{
		Matrix: versions.Matrix{Key: "k", Versions: []string{"1.0", line, line}},
		Policy: versions.PolicySummary{Labels: []string{line}},
	}
	outs, err := githubOutputs(rep)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range outs {
		got[o.name] = o.value
	}
	if got["matrix_json"] != `{"k":["1.0","1.1","1.1"]}` {
		t.Errorf("matrix_json = %s", got["matrix_json"])
	}
	if got["supported_versions"] != "1.0 1.1" {
		t.Errorf("supported_versions = %q", got["supported_versions"])
	}
}

// runnerCommands returns the workflow commands the Actions runner
// would run from log, as "name message" strings: lines starting with
// "::" outside a stop-commands block, and lines holding the legacy
// "##[" prefix anywhere.
func runnerCommands(log string) []string {
	var cmds []string
	stopped := ""
	for line := range strings.SplitSeq(log, "\n") {
		line = strings.TrimSpace(line)
		if stopped != "" {
			if line == "::"+stopped+"::" {
				stopped = ""
			}
			continue
		}
		if token, ok := strings.CutPrefix(line, "::stop-commands::"); ok {
			stopped = token
			continue
		}
		if rest, ok := strings.CutPrefix(line, "::"); ok {
			name, msg, _ := strings.Cut(rest, "::")
			cmds = append(cmds, name+" "+msg)
		} else if strings.Contains(line, "##[") {
			cmds = append(cmds, line)
		}
	}
	return cmds
}

// wantNoInjection fails when log would run a command whose message
// starts with "injected" or a legacy "##[" command.
func wantNoInjection(t *testing.T, what, log string) {
	t.Helper()
	for _, c := range runnerCommands(log) {
		_, msg, _ := strings.Cut(c, " ")
		if strings.HasPrefix(msg, "injected") || strings.Contains(c, "##[") {
			t.Errorf("%s runs the injected command %q:\n%s", what, c, log)
		}
	}
}

func TestGitHubErrorNotRaw(t *testing.T) {
	h := newHarness(t)
	githubEnv(t, h)
	policyPath := filepath.Join(t.TempDir(), "overlay.yaml")
	if err := os.WriteFile(policyPath,
		[]byte("schema: 1\ntools:\n  python:\n    policy:\n      \"\\n::error::injected\\n\": 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("--policy", policyPath, formatGitHubFlag, cmdMatrix, toolPython)
	r.want(t, ExitFailure)
	wantContains(t, "stdout", r.stdout, "::error::", "%0A::error::injected%0A")
	wantNoInjection(t, "stdout", r.stdout)
	wantNoInjection(t, "stderr", r.stderr)
	if strings.Contains(r.stderr, "injected") {
		t.Errorf("stderr carries the raw diagnostic:\n%s", r.stderr)
	}
}

func TestGitHubTextCommandsStopped(t *testing.T) {
	h := newHarness(t)
	githubEnv(t, h)
	var stdout bytes.Buffer
	e := &env{stdout: &stdout, stderr: &bytes.Buffer{}, getenv: func(k string) string { return h.vars[k] }}
	label := "stable\n::error::injected ##[error]legacy"
	rep := versions.Report{
		Tool:   toolPython,
		Name:   "Python",
		Source: versions.SourceSummary{Kind: versions.KindLive},
		Matrix: versions.Matrix{Key: "matrix-key", Versions: []string{"2.7", label}},
		Lines:  []versions.Line{{Line: "2.7", Status: "active", InMatrix: true, Reason: label}},
	}
	if err := e.emitGitHub(rep); err != nil {
		t.Fatal(err)
	}
	wantContains(t, "stdout", stdout.String(), "::stop-commands::svstop_", "\n::error::injected")
	wantNoInjection(t, "stdout", stdout.String())
}

// failText fails every write but workflow commands.
type failText struct{ bytes.Buffer }

func (w *failText) Write(b []byte) (int, error) {
	if !bytes.HasPrefix(b, []byte("::")) {
		return 0, errFailText
	}
	return w.Buffer.Write(b)
}

var errFailText = errors.New("write failed")

func TestWithoutCommandsResumes(t *testing.T) {
	var w failText
	e := &env{stdout: &w}
	err := e.withoutCommands(func() error {
		_, err := w.Write([]byte("text\n"))
		return err
	})
	if !errors.Is(err, errFailText) {
		t.Errorf("err = %v, want %v", err, errFailText)
	}
	lines := strings.Split(strings.TrimSuffix(w.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("log = %q, want a stop and a resume line", w.String())
	}
	token, ok := strings.CutPrefix(lines[0], "::stop-commands::")
	if !ok || len(token) < 20 || lines[1] != "::"+token+"::" {
		t.Errorf("log = %q, want a stop and a matching resume line", w.String())
	}
}
