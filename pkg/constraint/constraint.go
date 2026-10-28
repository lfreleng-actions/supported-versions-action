// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package constraint parses the version constraint a project declares
// and decides which release lines it allows.
//
// A constraint is evaluated per release line, because the matrix it
// narrows is a list of lines: each clause's version is cut to the
// scheme's line before it is compared. Apply narrows a matrix by a
// constraint and reports the outcome.
//
// The package depends on the standard library and pkg/release alone.
package constraint

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// ErrSyntax reports constraint text that does not parse.
var ErrSyntax = errors.New("invalid constraint")

// Constraint syntaxes Parse understands.
const (
	// SyntaxPEP440 is PEP 440 version specifiers with the Poetry
	// extensions: caret, tilde and "||" alternatives.
	SyntaxPEP440 = "pep440"
	// SyntaxGoDirective is a Go version, as a go.mod "go" or
	// "toolchain" directive names it, meaning that line or newer.
	SyntaxGoDirective = "go-directive"
)

// Outcome says what Apply did to a matrix.
type Outcome string

// Outcomes of Apply.
const (
	// OutcomeNone means there was no constraint to apply.
	OutcomeNone Outcome = "none"
	// OutcomeApplied means the matrix was narrowed to the lines the
	// constraint allows.
	OutcomeApplied Outcome = "applied"
	// OutcomeOutOfRangeFallback means the constraint allows no line of
	// the matrix, so the matrix is returned unchanged.
	OutcomeOutOfRangeFallback Outcome = "out-of-range-fallback"
)

// kind is the test a clause applies to a line.
type kind int

const (
	kindAny        kind = iota // every line
	kindCompare                // line compared with v by op
	kindEqual                  // line equals v
	kindNotEqual               // line differs from v
	kindMajor                  // line's major equals v's major
	kindNotMajor               // line's major differs from v's major
	kindCompatible             // line at least v, with the same major
	kindNone                   // no line
)

// clause is one normalized test on a line.
type clause struct {
	kind kind
	// op is the comparison operator of a kindCompare clause.
	op string
	// v is a release line, or for the major kinds a bare major.
	v string
	// text is the canonical form of the clause.
	text string
}

func (c clause) allows(line string) bool {
	switch c.kind {
	case kindCompare:
		r := release.CompareLines(line, c.v)
		switch c.op {
		case ">=":
			return r >= 0
		case ">":
			return r > 0
		case "<=":
			return r <= 0
		default:
			return r < 0
		}
	case kindEqual:
		return release.CompareLines(line, c.v) == 0
	case kindNotEqual:
		return release.CompareLines(line, c.v) != 0
	case kindMajor:
		return major(line) == major(c.v)
	case kindNotMajor:
		return major(line) != major(c.v)
	case kindCompatible:
		return major(line) == major(c.v) && release.CompareLines(line, c.v) >= 0
	case kindNone:
		return false
	default:
		return true
	}
}

// Constraint is a parsed constraint. Its zero value allows no line.
type Constraint struct {
	scheme string
	// alts are alternatives, any of which may allow a line; each is
	// a list of clauses, all of which must allow it.
	alts [][]clause
	// set, when non-nil, is the exact set of lines allowed.
	set  []string
	text string
}

// Allows reports whether the constraint allows the release line. A
// line that is not well formed under the constraint's scheme is never
// allowed.
func (c Constraint) Allows(line string) bool {
	if !release.ValidLine(line, c.scheme) {
		return false
	}
	if c.set != nil {
		return slices.Contains(c.set, line)
	}
	for _, alt := range c.alts {
		if allAllow(alt, line) {
			return true
		}
	}
	return false
}

func allAllow(clauses []clause, line string) bool {
	for _, cl := range clauses {
		if !cl.allows(line) {
			return false
		}
	}
	return true
}

// String returns the canonical form of the constraint, for reports:
// clauses without whitespace, a "v" prefix, a zero epoch or
// pre-release, post-release and development suffixes, joined by ", ",
// alternatives joined by " || ", and a line set as its sorted lines
// in braces. A non-zero epoch and a local part are kept.
func (c Constraint) String() string {
	return c.text
}

// Parse parses constraint text written in syntax for a tool whose
// release lines follow scheme. Text that does not parse returns an
// error wrapping ErrSyntax, as does pep440 text that is empty or holds
// nothing but commas and whitespace.
func Parse(syntax, scheme, text string) (Constraint, error) {
	if !knownScheme(scheme) {
		return Constraint{}, fmt.Errorf("unknown version scheme %q", scheme)
	}
	switch syntax {
	case SyntaxPEP440:
		return parsePEP440(scheme, text)
	case SyntaxGoDirective:
		return parseGoDirective(scheme, text)
	default:
		return Constraint{}, fmt.Errorf("unsupported constraint syntax %q", syntax)
	}
}

// NewLineSet returns a constraint that allows exactly the lines given.
// Each must be a well-formed line under scheme, and there must be at
// least one.
func NewLineSet(scheme string, lines []string) (Constraint, error) {
	if !knownScheme(scheme) {
		return Constraint{}, fmt.Errorf("unknown version scheme %q", scheme)
	}
	if len(lines) == 0 {
		return Constraint{}, fmt.Errorf("%w: empty line set", ErrSyntax)
	}
	set := make([]string, 0, len(lines))
	for _, l := range lines {
		if !release.ValidLine(l, scheme) {
			return Constraint{}, fmt.Errorf("%w: %q is not a %s line", ErrSyntax, l, scheme)
		}
		if !slices.Contains(set, l) {
			set = append(set, l)
		}
	}
	slices.SortFunc(set, release.CompareLines)
	return Constraint{scheme: scheme, set: set, text: "{" + strings.Join(set, ", ") + "}"}, nil
}

// Apply narrows matrix to the lines c allows, keeping their order. A
// nil c leaves the matrix unchanged with OutcomeNone. When c allows no
// line of the matrix, the matrix is returned unchanged with
// OutcomeOutOfRangeFallback; rejecting it instead is the caller's
// choice. The result never shares storage with matrix.
func Apply(matrix []string, c *Constraint) (lines []string, outcome Outcome) {
	if c == nil {
		return slices.Clone(matrix), OutcomeNone
	}
	for _, l := range matrix {
		if c.Allows(l) {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return slices.Clone(matrix), OutcomeOutOfRangeFallback
	}
	return lines, OutcomeApplied
}

func knownScheme(scheme string) bool {
	return scheme == release.SchemeMajor || scheme == release.SchemeMajorMinor
}

// major returns the first component of a line.
func major(line string) string {
	m, _, _ := strings.Cut(line, ".")
	return m
}

// cut returns the release line of numeric components under scheme,
// extending a single component with a zero under SchemeMajorMinor.
func cut(scheme string, parts []string) string {
	if scheme == release.SchemeMajor {
		return parts[0]
	}
	if len(parts) == 1 {
		return parts[0] + ".0"
	}
	return parts[0] + "." + parts[1]
}

// goVersion is the version grammar golang.org/x/mod/modfile accepts
// for a go directive, limited to Go's compact prerelease markers:
// major, minor, an optional patch and an optional prerelease.
var goVersion = regexp.MustCompile(`^[1-9][0-9]*\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?((rc|beta|alpha)[1-9][0-9]*)?$`)

func parseGoDirective(scheme, text string) (Constraint, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return Constraint{}, fmt.Errorf("%w: empty text", ErrSyntax)
	}
	if !goVersion.MatchString(strings.TrimPrefix(t, "go")) {
		return Constraint{}, fmt.Errorf("%w: %q is not a Go version", ErrSyntax, text)
	}
	v, err := release.Parse(scheme, t)
	if err != nil {
		return Constraint{}, fmt.Errorf("%w: %w", ErrSyntax, err)
	}
	cl := clause{kind: kindCompare, op: ">=", v: v.Line, text: ">=" + v.Line}
	return Constraint{scheme: scheme, alts: [][]clause{{cl}}, text: cl.text}, nil
}

func parsePEP440(scheme, text string) (Constraint, error) {
	if strings.Trim(text, ", \t\r\n") == "" {
		return Constraint{}, fmt.Errorf("%w: empty text", ErrSyntax)
	}
	var alts [][]clause
	var altTexts []string
	for alt := range strings.SplitSeq(text, "||") {
		var clauses []clause
		var clauseTexts []string
		for raw := range strings.SplitSeq(alt, ",") {
			// Empty clauses are skipped, as packaging skips them.
			if strings.TrimSpace(raw) == "" {
				continue
			}
			cl, err := parseClause(scheme, raw)
			if err != nil {
				return Constraint{}, fmt.Errorf("%w: %q: %w", ErrSyntax, text, err)
			}
			clauses = append(clauses, cl)
			clauseTexts = append(clauseTexts, cl.text)
		}
		if len(clauses) == 0 {
			return Constraint{}, fmt.Errorf("%w: %q: empty alternative", ErrSyntax, text)
		}
		alts = append(alts, clauses)
		altTexts = append(altTexts, strings.Join(clauseTexts, ", "))
	}
	return Constraint{scheme: scheme, alts: alts, text: strings.Join(altTexts, " || ")}, nil
}

// operators are tried in order, so each comes before any operator
// that is its prefix.
var operators = []string{"===", "==", "!=", "<=", ">=", "~=", "<", ">", "^", "~"}

// clauseVersion is a PEP 440 version, optionally ending in a ".*"
// wildcard. Group 1 holds the epoch, group 2 the numeric release,
// group 3 the wildcard, group 4 any pre-release, post-release or
// development suffix and group 5 the local part.
var clauseVersion = regexp.MustCompile(`^(?i)v?(?:([0-9]+)!)?([0-9]+(?:\.[0-9]+)*)(\.\*)?` +
	`((?:[-_.]?(?:a|b|c|rc|alpha|beta|pre|preview)[-_.]?[0-9]*)?` +
	`(?:-[0-9]+|[-_.]?(?:post|rev|r)[-_.]?[0-9]*)?` +
	`(?:[-_.]?dev[-_.]?[0-9]*)?)` +
	`(?:\+([a-z0-9]+(?:[-_.][a-z0-9]+)*))?$`)

// localSeparators are the separators of a local version part, which
// PEP 440 normalizes to dots.
var localSeparators = strings.NewReplacer("-", ".", "_", ".")

func parseClause(scheme, raw string) (clause, error) {
	s := strings.TrimSpace(raw)
	op := ""
	for _, o := range operators {
		if strings.HasPrefix(s, o) {
			op = o
			break
		}
	}
	if op == "" {
		return clause{}, fmt.Errorf("clause %q: unknown operator", s)
	}
	ver := strings.TrimSpace(s[len(op):])
	if op == "===" {
		// Arbitrary equality compares strings, so it never matches
		// a release line.
		if strings.ContainsAny(ver, " \t\r\n;)") {
			return clause{}, fmt.Errorf("clause %q: invalid version %q", s, ver)
		}
		return clause{kind: kindNone, text: op + ver}, nil
	}
	if ver == "" {
		return clause{}, fmt.Errorf("clause %q: missing version", s)
	}
	v, ok := parseVersion(ver)
	if !ok {
		return clause{}, fmt.Errorf("clause %q: invalid version %q", s, ver)
	}
	if err := v.check(op); err != nil {
		return clause{}, fmt.Errorf("clause %q: %w", s, err)
	}
	var cl clause
	switch {
	case v.epoch != "":
		cl = epochClause(op)
	case v.local != "":
		cl = localClause(op)
	default:
		cl = clauseFor(scheme, op, v.parts, v.wildcard)
	}
	cl.text = op + v.String()
	return cl, nil
}

// version is a clause's PEP 440 version, normalized.
type version struct {
	// epoch is the epoch, or "" for the default epoch, zero.
	epoch string
	// parts are the numeric release components.
	parts    []string
	wildcard bool
	// suffix is any pre-release, post-release or development suffix.
	suffix string
	// local is the local part, with its separators made dots.
	local string
}

// parseVersion parses a clause's version, dropping a leading "v" and
// the leading zeros of each number.
func parseVersion(ver string) (version, bool) {
	m := clauseVersion.FindStringSubmatch(ver)
	if m == nil {
		return version{}, false
	}
	v := version{wildcard: m[3] != "", suffix: m[4]}
	if m[1] != "" && trimZeros(m[1]) != "0" {
		v.epoch = trimZeros(m[1])
	}
	v.parts = strings.Split(m[2], ".")
	for i, part := range v.parts {
		v.parts[i] = trimZeros(part)
	}
	if m[5] != "" {
		v.local = localSeparators.Replace(strings.ToLower(m[5]))
	}
	return v, true
}

// check reports a version that PEP 440 does not allow with op.
func (v version) check(op string) error {
	exact := op == "==" || op == "!="
	switch {
	case v.wildcard && (v.suffix != "" || v.local != "" || !exact):
		return errors.New("misplaced wildcard")
	case v.local != "" && !exact:
		return errors.New("local version needs == or !=")
	case op == "~=" && len(v.parts) < 2:
		return errors.New("compatible release needs at least two components")
	}
	return nil
}

// String returns the canonical form of the version: no "v", no zero
// epoch, and no suffix but a wildcard or a local part.
func (v version) String() string {
	s := strings.Join(v.parts, ".")
	if v.epoch != "" {
		s = v.epoch + "!" + s
	}
	if v.wildcard {
		s += ".*"
	}
	if v.local != "" {
		s += "+" + v.local
	}
	return s
}

// epochClause returns the line test for operator op on a version with
// a non-zero epoch. Release lines carry the default epoch, zero, and
// epochs compare first, so every line is older than such a version.
func epochClause(op string) clause {
	switch op {
	case "<", "<=", "!=":
		return clause{kind: kindAny}
	default:
		return clause{kind: kindNone}
	}
}

// localClause returns the line test for operator op, == or !=, on a
// version with a local part. Such a version names one local build: it
// is never a whole line, and excluding it leaves its line allowed.
func localClause(op string) clause {
	if op == "!=" {
		return clause{kind: kindAny}
	}
	return clause{kind: kindNone}
}

// trimZeros strips the leading zeros PEP 440 permits from a numeric
// component, keeping one zero for a component that is all zeros, so
// that "3.011" means 3.11 and release lines compare correctly.
func trimZeros(part string) string {
	if t := strings.TrimLeft(part, "0"); t != "" {
		return t
	}
	return "0"
}

// clauseFor returns the line test for operator op on a version with
// the numeric components parts, ending in a wildcard if wildcard.
func clauseFor(scheme, op string, parts []string, wildcard bool) clause {
	line := cut(scheme, parts)
	exact := clause{kind: kindEqual, v: line}
	switch op {
	case "==":
		if wildcard && len(parts) == 1 {
			return clause{kind: kindMajor, v: parts[0]}
		}
		return exact
	case "!=":
		if len(parts) > len(strings.Split(line, ".")) {
			// Excluding one release, or one series below a line,
			// leaves the rest of its line allowed.
			return clause{kind: kindAny}
		}
		if wildcard && len(parts) == 1 {
			return clause{kind: kindNotMajor, v: parts[0]}
		}
		return clause{kind: kindNotEqual, v: line}
	case "~=":
		if len(parts) == 2 {
			return clause{kind: kindCompatible, v: line}
		}
		return exact
	case "^":
		if parts[0] == "0" && len(parts) > 1 {
			return exact
		}
		return clause{kind: kindCompatible, v: line}
	case "~":
		if len(parts) == 1 {
			return clause{kind: kindMajor, v: parts[0]}
		}
		return exact
	default:
		return clause{kind: kindCompare, op: op, v: line}
	}
}
