// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package constraint

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// SyntaxSemverRange is the npm and node-semver range grammar that a
// package.json "engines" field uses: comparators, hyphen ranges, "||"
// unions, caret and tilde ranges, x-ranges and space-joined
// intersections.
const SyntaxSemverRange = "semver-range"

// interval is the set of stable versions a semver comparator set
// allows: from lo, inclusive, up to hi, exclusive, where an empty end
// is unbounded. Both ends are full stable versions, three
// dot-separated numbers without leading zeros. Versions are discrete,
// so the interval is empty when lo is not below hi.
//
// Only stable versions count. node-semver lets a prerelease satisfy a
// range only when a comparator names a prerelease of the same version,
// and the matrix installs released versions, so a line whose sole
// matching versions are prereleases is not allowed.
type interval struct {
	lo, hi string
	empty  bool
}

// allows reports whether some stable version of the release line
// falls in the interval. A line holds every version that starts with
// its components.
func (iv interval) allows(line string) bool {
	if iv.empty {
		return false
	}
	p := partial(strings.Split(line, "."))
	if len(p) > 3 {
		return false
	}
	end, err := p.next(len(p) - 1)
	if err != nil {
		return false
	}
	low := p.full()
	if iv.lo != "" && release.CompareLines(iv.lo, low) > 0 {
		low = iv.lo
	}
	if iv.hi != "" && release.CompareLines(iv.hi, end) < 0 {
		end = iv.hi
	}
	return release.CompareLines(low, end) < 0
}

// intersect narrows iv to the versions other also allows.
func (iv interval) intersect(other interval) interval {
	if iv.empty || other.empty {
		return interval{empty: true}
	}
	out := iv
	if other.lo != "" && (out.lo == "" || release.CompareLines(other.lo, out.lo) > 0) {
		out.lo = other.lo
	}
	if other.hi != "" && (out.hi == "" || release.CompareLines(other.hi, out.hi) < 0) {
		out.hi = other.hi
	}
	return out
}

var (
	// hyphenRange is "A - B", with whitespace around the hyphen.
	hyphenRange = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)$`)
	// comparatorTrim, tildeTrim and caretTrim are node-semver's
	// COMPARATORTRIM, TILDETRIM and CARETTRIM, which join an operator
	// to the version after it. A comparison operator joins only
	// across the whitespace straight after it, so ">= =22" is
	// ">==22" but ">== 22" stays split and is invalid.
	comparatorTrim = regexp.MustCompile(`(\s*)((?:<|>)?=?)\s*([v=\s]*[0-9xX*])`)
	tildeTrim      = regexp.MustCompile(`(\s*)(~>?)\s+`)
	caretTrim      = regexp.MustCompile(`(\s*)\^\s+`)
	// comparator is an optional operator, a lead of "v" and "="
	// characters, and a partial version. Group 1 holds the operator;
	// group 2 the lead; groups 3 to 5 the major, minor and patch, each
	// a number or a wildcard; group 6 the prerelease identifiers. A
	// numeric prerelease identifier has no leading zero.
	comparator = regexp.MustCompile(`^(<=|>=|~>|[<>=~^])?([v=]*)` +
		`(0|[1-9][0-9]*|[xX*])(?:\.(0|[1-9][0-9]*|[xX*])(?:\.(0|[1-9][0-9]*|[xX*])` +
		`(?:-(` + preIdent + `(?:\.` + preIdent + `)*))?(?:` + buildMeta + `)?)?)?$`)
	// buildPattern finds build metadata, which node-semver strips from
	// each comparator before parsing it.
	buildPattern = regexp.MustCompile(buildMeta)
)

const (
	// preIdent is one prerelease identifier: a number without leading
	// zeros, or digits, letters and hyphens holding a non-digit.
	preIdent = `(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)`
	// buildMeta is build metadata, whose identifiers may lead with zeros.
	buildMeta = `\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*`
)

// partial is a version with zero to three numeric components; the
// components after the first wildcard or absent one are unknown.
type partial []string

// parsePartial reads the version groups of a comparator match. Like
// node-semver, it drops every component after the first wildcard, so
// "x.22" is "*" and "22.x.5" is "22.x".
func parsePartial(groups []string) partial {
	var p partial
	for _, g := range groups {
		if g == "" || g == "x" || g == "X" || g == "*" {
			break
		}
		p = append(p, g)
	}
	return p
}

// full pads p with zeros to a full version.
func (p partial) full() string {
	parts := append([]string(nil), p...)
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return strings.Join(parts, ".")
}

// next returns the smallest full version above every version p
// matches when the component at index i is raised by one: next(0) of
// 18.4 is 19.0.0.
func (p partial) next(i int) (string, error) {
	n, err := strconv.ParseUint(p[i], 10, 63)
	if err != nil {
		return "", fmt.Errorf("version component %q is too large", p[i])
	}
	parts := append([]string(nil), p[:i]...)
	parts = append(parts, strconv.FormatUint(n+1, 10))
	return partial(parts).full(), nil
}

// after returns the smallest stable version above every version p
// matches. For a full version that is the next patch, unless p carries
// a prerelease: the prerelease sorts below p's stable version, so that
// version is the one after it.
func (p partial) after(pre bool) (string, error) {
	if pre {
		return p.full(), nil
	}
	return p.next(len(p) - 1)
}

// matchVersion reads the partial version of a comparator match and
// whether it carries a prerelease. node-semver ignores a prerelease
// unless all three components are numbers, so "22.x.x-rc.1" is "22.x".
func matchVersion(m []string) (partial, bool) {
	p := parsePartial(m[3:6])
	return p, m[6] != "" && len(p) == 3
}

// plainLead reports whether lead may precede a full version that
// node-semver reads as written rather than rebuilds from its
// components. Its x-range, caret and tilde grammars take any run of
// "v" and "=" before a version, but they pass a full version through
// unchanged to its comparator grammar, which takes at most one "v".
// So "==22" is "22" while "==22.0.0" and ">==22.0.0" are invalid.
func plainLead(lead string) bool {
	return lead == "" || lead == "v"
}

// comparatorInterval turns one comparator into an interval of stable
// versions, following node-semver's desugaring of partial versions.
// pre reports whether the version carries a prerelease, which only a
// full version can.
func comparatorInterval(op string, p partial, pre bool) (interval, error) {
	if len(p) == 0 {
		switch op {
		case "<", ">":
			return interval{empty: true}, nil
		default:
			return interval{}, nil
		}
	}
	lo := p.full()
	switch op {
	case "", "=":
		hi, err := p.after(pre)
		return interval{lo: lo, hi: hi}, err
	case ">=":
		return interval{lo: lo}, nil
	case ">":
		v, err := p.after(pre)
		return interval{lo: v}, err
	case "<":
		return interval{hi: lo}, nil
	case "<=":
		hi, err := p.after(pre)
		return interval{hi: hi}, err
	case "~", "~>":
		i := min(len(p)-1, 1)
		hi, err := p.next(i)
		return interval{lo: lo, hi: hi}, err
	case "^":
		// The first non-zero component is the one that may not
		// change; when every known one is zero, the last known one.
		i := len(p) - 1
		for j, c := range p {
			if c != "0" {
				i = j
				break
			}
		}
		hi, err := p.next(i)
		return interval{lo: lo, hi: hi}, err
	default:
		return interval{}, fmt.Errorf("unknown operator %q", op)
	}
}

// parseComparator parses one comparator of a range. Like node-semver,
// it first strips the first build metadata it finds, so a partial
// version such as "22.x+build" is accepted; hyphen range ends are not
// stripped and take build metadata only after a full version.
func parseComparator(s string) (interval, error) {
	stripped := s
	if loc := buildPattern.FindStringIndex(s); loc != nil {
		stripped = s[:loc[0]] + s[loc[1]:]
	}
	m := comparator.FindStringSubmatch(stripped)
	if m == nil {
		return interval{}, fmt.Errorf("comparator %q: invalid", s)
	}
	p, pre := matchVersion(m)
	if len(p) == 3 && !strings.ContainsAny(m[1], "~^") && !plainLead(m[2]) {
		return interval{}, fmt.Errorf("comparator %q: invalid operator", s)
	}
	iv, err := comparatorInterval(m[1], p, pre)
	if err != nil {
		return interval{}, fmt.Errorf("comparator %q: %w", s, err)
	}
	return iv, nil
}

// parseHyphen parses "A - B": from A, inclusive, to B, inclusive of
// every version B matches.
func parseHyphen(from, to string) (interval, string, error) {
	lo, err := parseHyphenEnd(from, false)
	if err != nil {
		return interval{}, "", err
	}
	hi, err := parseHyphenEnd(to, true)
	if err != nil {
		return interval{}, "", err
	}
	return interval{lo: lo.lo, hi: hi.hi}, from + " - " + to, nil
}

// parseHyphenEnd parses one end of a hyphen range as the interval of
// the version, which takes no operator but may lead with "v" and "=".
// node-semver keeps a full end as written, and so takes only a plain
// lead there, except on an upper end with a prerelease, which it
// rebuilds from the components.
func parseHyphenEnd(s string, upper bool) (interval, error) {
	m := comparator.FindStringSubmatch(s)
	if m == nil || (m[1] != "" && m[1] != "=") {
		return interval{}, fmt.Errorf("hyphen range end %q: invalid", s)
	}
	p, pre := matchVersion(m)
	if len(p) == 3 && (!upper || !pre) && !plainLead(m[1]+m[2]) {
		return interval{}, fmt.Errorf("hyphen range end %q: invalid lead", s)
	}
	iv, err := comparatorInterval("", p, pre)
	if err != nil {
		return interval{}, fmt.Errorf("hyphen range end %q: %w", s, err)
	}
	return iv, nil
}

// parseRange parses one alternative of a range set: a hyphen range,
// or comparators joined by whitespace. An empty alternative allows
// every version, as in node-semver.
func parseRange(text string) (interval, string, error) {
	t := strings.TrimSpace(text)
	if m := hyphenRange.FindStringSubmatch(t); m != nil {
		return parseHyphen(m[1], m[2])
	}
	t = comparatorTrim.ReplaceAllString(t, "$1$2$3")
	t = tildeTrim.ReplaceAllString(t, "$1$2")
	t = caretTrim.ReplaceAllString(t, "$1^")
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return interval{}, "*", nil
	}
	var iv interval
	for _, f := range fields {
		c, err := parseComparator(f)
		if err != nil {
			return interval{}, "", err
		}
		iv = iv.intersect(c)
	}
	return iv, strings.Join(fields, " "), nil
}

// parseSemverRange parses a node-semver range set. A line is allowed
// when some version of it satisfies the range: ">=18.17.0" allows
// line 18. Text that is empty, or nothing but whitespace, is an error.
func parseSemverRange(scheme, text string) (Constraint, error) {
	if strings.TrimSpace(text) == "" {
		return Constraint{}, fmt.Errorf("%w: empty text", ErrSyntax)
	}
	var alts [][]clause
	var texts []string
	for alt := range strings.SplitSeq(text, "||") {
		iv, t, err := parseRange(alt)
		if err != nil {
			return Constraint{}, fmt.Errorf("%w: %q: %w", ErrSyntax, text, err)
		}
		alts = append(alts, []clause{{kind: kindInterval, iv: iv, text: t}})
		texts = append(texts, t)
	}
	return Constraint{scheme: scheme, alts: alts, text: strings.Join(texts, " || ")}, nil
}
