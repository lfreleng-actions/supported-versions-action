// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

// output is one GitHub Actions step output.
type output struct {
	name  string
	value string
}

// emitGitHub writes the step outputs, the warnings as annotations, the
// text form of the report to the log with workflow commands stopped
// and, when GITHUB_STEP_SUMMARY is set, a section of the job summary.
func (e *env) emitGitHub(rep versions.Report) error {
	outs, err := githubOutputs(rep)
	if err != nil {
		return err
	}
	reportPath, err := e.writeReportFile(rep)
	if err != nil {
		return fmt.Errorf("report file: %w", err)
	}
	outs = append(outs, output{"report_path", reportPath})
	if c := rep.Check; c != nil {
		outs = append(outs,
			output{"status", string(c.Status)},
			output{"supported", strconv.FormatBool(c.Supported)},
			output{"policy_excluded", strconv.FormatBool(c.PolicyExcluded)},
			output{"eol_date", c.EOL},
		)
	}
	if err = appendFile(e.getenv("GITHUB_OUTPUT"), encodeOutputs(outs)); err != nil {
		return fmt.Errorf("GITHUB_OUTPUT: %w", err)
	}
	p := &printer{w: e.stdout}
	for _, w := range rep.Warnings {
		p.printf("::warning::%s\n", escapeAnnotation(w))
	}
	if p.err != nil {
		return p.err
	}
	if err = e.withoutCommands(func() error { return writeText(e.stdout, rep) }); err != nil {
		return err
	}
	if path := e.getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if err = appendFile(path, summary(rep)); err != nil {
			return fmt.Errorf("GITHUB_STEP_SUMMARY: %w", err)
		}
	}
	return nil
}

// githubOutputs returns the outputs every report sets, but report_path.
func githubOutputs(rep versions.Report) ([]output, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string][]string{rep.Matrix.Key: rep.Matrix.Versions}); err != nil {
		return nil, err
	}
	var eol []string
	for _, l := range rep.Lines {
		if l.InMatrix && (l.Status == release.StatusEOL || l.Status == release.StatusExtended) {
			eol = append(eol, l.Line)
		}
	}
	src := string(rep.Source.Kind)
	if rep.Source.Kind == versions.KindSnapshot && rep.Source.SnapshotDate != "" {
		src = "snapshot:" + rep.Source.SnapshotDate
	}
	return []output{
		{"matrix_json", strings.TrimSuffix(buf.String(), "\n")},
		{"supported_versions", strings.Join(matrixLines(rep), " ")},
		{"latest", rep.Latest},
		{"eol_versions", strings.Join(eol, " ")},
		{"versions_source", src},
	}, nil
}

// matrixLines returns the release lines of the matrix without the
// policy labels. versions.Matrix appends the labels after the lines, so
// they are dropped by position: a label may equal a release line.
func matrixLines(rep versions.Report) []string {
	n := max(len(rep.Matrix.Versions)-len(rep.Policy.Labels), 0)
	return rep.Matrix.Versions[:n]
}

// encodeOutputs writes each output in the multi-line form, with a
// random delimiter absent from the value, so that no value can inject
// another output.
func encodeOutputs(outs []output) []byte {
	var buf bytes.Buffer
	for _, o := range outs {
		delim := "ghadelim_" + rand.Text()
		for strings.Contains(o.value, delim) {
			delim = "ghadelim_" + rand.Text()
		}
		fmt.Fprintf(&buf, "%s<<%s\n%s\n%s\n", o.name, delim, o.value, delim)
	}
	return buf.Bytes()
}

// writeReportFile writes the JSON report under $RUNNER_TEMP and returns
// its path, or "" without RUNNER_TEMP.
func (e *env) writeReportFile(rep versions.Report) (path string, err error) {
	tmp := e.getenv("RUNNER_TEMP")
	if tmp == "" {
		return "", nil
	}
	data, err := rep.JSON()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(tmp, "supported-versions")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if err = os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory needs its search bit
		return "", err
	}
	// CreateTemp creates the file with mode 0600.
	f, err := os.CreateTemp(dir, "report-"+rep.Tool+"-*.json")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(f.Name()) //nolint:errcheck // Best-effort cleanup.
		}
	}()
	if _, err = f.Write(data); err != nil {
		return "", errors.Join(err, f.Close())
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// appendFile appends data to the file at path, creating it if absent.
func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- a path the runner names
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

// withoutCommands runs fn with workflow command processing stopped, so
// that no line fn prints to the log runs as a command, and resumes it
// even when fn fails. The token is random so that the text cannot
// resume processing itself.
func (e *env) withoutCommands(fn func() error) (err error) {
	token := "svstop_" + rand.Text()
	if _, err = fmt.Fprintf(e.stdout, "::stop-commands::%s\n", token); err != nil {
		return err
	}
	defer func() {
		_, resumeErr := fmt.Fprintf(e.stdout, "::%s::\n", token)
		err = errors.Join(err, resumeErr)
	}()
	return fn()
}

// escapeAnnotation escapes a workflow command message.
func escapeAnnotation(s string) string {
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

// summary renders the job summary section of rep.
func summary(rep versions.Report) []byte {
	var buf bytes.Buffer
	codes := make([]string, 0, len(rep.Matrix.Versions))
	for _, v := range rep.Matrix.Versions {
		codes = append(codes, "`"+cell(v)+"`")
	}
	fmt.Fprintf(&buf, "## %s (`%s`)\n\n", cell(rep.Name), rep.Tool)
	fmt.Fprintf(&buf, "- **Matrix** (`%s`): %s\n", rep.Matrix.Key, dash(strings.Join(codes, " ")))
	fmt.Fprintf(&buf, "- **Latest:** %s\n", dash(rep.Latest))
	fmt.Fprintf(&buf, "- **Source:** %s\n", sourceText(rep.Source))
	if c := rep.Constraint; c != nil {
		fmt.Fprintf(&buf, "- **Constraint:** `%s` (%s)\n", cell(c.Text), c.Outcome)
	}
	if c := rep.Check; c != nil {
		verdict := "is not supported"
		if c.Supported {
			verdict = "is supported"
		}
		fmt.Fprintf(&buf, "- **Check:** `%s` %s (status %s)\n", cell(c.Version), verdict, c.Status)
	}
	buf.WriteString("\n| Line | Status | In matrix | Reason | Newest | EOL |\n")
	buf.WriteString("| ---- | ------ | --------- | ------ | ------ | --- |\n")
	for _, l := range rep.Lines {
		fmt.Fprintf(&buf, "| %s | %s | %s | %s | %s | %s |\n",
			l.Line, l.Status, yesNo(l.InMatrix), dash(l.Reason), dash(l.Newest), dash(l.EOL))
	}
	buf.WriteString("\n")
	return buf.Bytes()
}

// cell keeps text from breaking a Markdown table row or code span.
func cell(s string) string {
	return strings.NewReplacer("|", `\|`, "`", "'", "\n", " ", "\r", " ").Replace(s)
}
