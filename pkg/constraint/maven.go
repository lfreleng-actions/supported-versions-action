// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package constraint

import (
	"cmp"
	"fmt"
	"regexp"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// SyntaxMavenRange is Maven's version range syntax, as a POM's
// prerequisites or the Maven Enforcer plugin write it: bounded ranges
// such as "[3.8,4.0)", half-open ones such as "[3.6.3,)" or "(,3.9]",
// an exact "[3.9.6]", unions of ranges joined by commas, and a bare
// version, a soft requirement meaning that version or newer.
const SyntaxMavenRange = "maven-range"

// mavenVersion is a version in a Maven range: one to three numbers
// and an optional pre-release qualifier, the kinds Maven Central
// publishes Maven itself with (-alpha-2, -beta-1, -M1, -rc-7) and
// -SNAPSHOT. Group 1 holds the numbers and group 2 the qualifier.
var mavenVersion = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+){0,2})` +
	`(-(?i:(?:alpha|beta|milestone|rc|cr)[.-]?[0-9]+|m[0-9]+|snapshot))?$`)

// mavenQualifier splits a qualifier group 2 of mavenVersion holds,
// lowercased, into its kind and its number.
var mavenQualifier = regexp.MustCompile(`^-(alpha|beta|milestone|m|rc|cr|snapshot)[.-]?([0-9]*)$`)

// mavenRanks orders the qualifier kinds as Maven's ComparableVersion
// does, every one of them before the release itself.
var mavenRanks = map[string]int{
	"alpha": 0, "beta": 1, "milestone": 2, "m": 2, "rc": 3, "cr": 3, "snapshot": 4,
}

// mavenRelease is the rank of a version with no qualifier.
const mavenRelease = 5

// mavenBound is one version of a Maven range: its numeric components
// padded to three, and the rank and number of its qualifier.
type mavenBound struct {
	p    partial
	rank int
	n    string
}

func parseMavenVersion(s string) (mavenBound, error) {
	m := mavenVersion.FindStringSubmatch(s)
	if m == nil {
		return mavenBound{}, fmt.Errorf("%q is not a Maven version", s)
	}
	parts := strings.Split(m[1], ".")
	for i, part := range parts {
		parts[i] = trimZeros(part)
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	b := mavenBound{p: partial(parts), rank: mavenRelease}
	if q := mavenQualifier.FindStringSubmatch(strings.ToLower(m[2])); q != nil {
		b.rank, b.n = mavenRanks[q[1]], trimZeros(q[2])
	}
	return b, nil
}

// pre reports whether b carries a pre-release qualifier.
func (b mavenBound) pre() bool {
	return b.rank != mavenRelease
}

// compare orders two bounds as Maven does: by number, then by the
// qualifier's kind, alpha before beta before milestone before rc
// before snapshot before the release, then by the qualifier's number.
func (b mavenBound) compare(o mavenBound) int {
	if c := release.CompareLines(b.p.full(), o.p.full()); c != 0 {
		return c
	}
	if c := cmp.Compare(b.rank, o.rank); c != 0 {
		return c
	}
	return cmp.Or(cmp.Compare(len(b.n), len(o.n)), strings.Compare(b.n, o.n))
}

// after is the smallest stable version above b.
func (b mavenBound) after() (string, error) {
	return b.p.after(b.pre())
}

// mavenRange is one parsed range of a union.
type mavenRange struct {
	iv   interval
	text string
}

// parseMavenRange parses the range text[0:] starts with, which opens
// with "[" or "(", and returns it and the text after it.
func parseMavenRange(text string) (mavenRange, string, error) {
	end := strings.IndexAny(text, "])")
	if end < 0 {
		return mavenRange{}, "", fmt.Errorf("range %q is not closed", text)
	}
	opening, closing, body := text[0], text[end], text[1:end]
	if strings.ContainsAny(body, "[(") {
		return mavenRange{}, "", fmt.Errorf("range %q is not closed", text[:end+1])
	}
	rest := text[end+1:]
	parts := strings.Split(body, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	switch len(parts) {
	case 1:
		r, err := exactMavenRange(opening, closing, parts[0])
		return r, rest, err
	case 2:
		r, err := boundedMavenRange(opening, closing, parts[0], parts[1])
		return r, rest, err
	default:
		return mavenRange{}, "", fmt.Errorf("range %q has more than two bounds", text[:end+1])
	}
}

// exactMavenRange parses "[v]", which allows v alone.
func exactMavenRange(opening, closing byte, v string) (mavenRange, error) {
	text := string(opening) + v + string(closing)
	if opening != '[' || closing != ']' {
		return mavenRange{}, fmt.Errorf("single-version range %q must be written [version]", text)
	}
	b, err := parseMavenVersion(v)
	if err != nil {
		return mavenRange{}, fmt.Errorf("range %q: %w", text, err)
	}
	hi, err := b.after()
	if err != nil {
		return mavenRange{}, fmt.Errorf("range %q: %w", text, err)
	}
	return mavenRange{iv: interval{lo: b.p.full(), hi: hi}, text: text}, nil
}

// boundedMavenRange parses a range with a lower and an upper bound,
// either of which may be empty for an unbounded side.
func boundedMavenRange(opening, closing byte, lo, hi string) (mavenRange, error) {
	text := string(opening) + lo + "," + hi + string(closing)
	if lo == "" && hi == "" {
		return mavenRange{}, fmt.Errorf("range %q has no bound", text)
	}
	var iv interval
	var low, high mavenBound
	var err error
	if lo != "" {
		if low, err = parseMavenVersion(lo); err != nil {
			return mavenRange{}, fmt.Errorf("range %q: %w", text, err)
		}
		iv.lo = low.p.full()
		if opening == '(' {
			if iv.lo, err = low.after(); err != nil {
				return mavenRange{}, fmt.Errorf("range %q: %w", text, err)
			}
		}
	}
	if hi != "" {
		if high, err = parseMavenVersion(hi); err != nil {
			return mavenRange{}, fmt.Errorf("range %q: %w", text, err)
		}
		iv.hi = high.p.full()
		if closing == ']' {
			if iv.hi, err = high.after(); err != nil {
				return mavenRange{}, fmt.Errorf("range %q: %w", text, err)
			}
		}
	}
	if lo != "" && hi != "" {
		c := low.compare(high)
		if c > 0 || (c == 0 && (opening != '[' || closing != ']')) {
			return mavenRange{}, fmt.Errorf("range %q defies version ordering", text)
		}
	}
	return mavenRange{iv: iv, text: text}, nil
}

// parseMavenRangeSet parses Maven range text. A line is allowed when
// some stable version of it is in one of the ranges: "[3.6.3,)"
// allows line 3.6, and "(,3.9]" allows 3.9, through 3.9.0. A bare
// version is a soft requirement that Maven reads as a preference; as
// a constraint it means that version or newer, so "3.9.6" allows 3.9
// and every later line. Text that is empty or blank is an error.
func parseMavenRangeSet(scheme, text string) (Constraint, error) {
	t := strings.TrimSpace(text)
	if t == "" {
		return Constraint{}, fmt.Errorf("%w: empty text", ErrSyntax)
	}
	if t[0] != '[' && t[0] != '(' {
		b, err := parseMavenVersion(t)
		if err != nil {
			return Constraint{}, fmt.Errorf("%w: %w", ErrSyntax, err)
		}
		cl := clause{kind: kindInterval, iv: interval{lo: b.p.full()}, text: t}
		return Constraint{scheme: scheme, alts: [][]clause{{cl}}, text: t}, nil
	}
	var alts [][]clause
	var texts []string
	rest := t
	for {
		r, after, err := parseMavenRange(rest)
		if err != nil {
			return Constraint{}, fmt.Errorf("%w: %q: %w", ErrSyntax, text, err)
		}
		alts = append(alts, []clause{{kind: kindInterval, iv: r.iv, text: r.text}})
		texts = append(texts, r.text)
		rest = strings.TrimSpace(after)
		if rest == "" {
			break
		}
		next, ok := strings.CutPrefix(rest, ",")
		next = strings.TrimSpace(next)
		if !ok || next == "" || (next[0] != '[' && next[0] != '(') {
			return Constraint{}, fmt.Errorf("%w: %q: expected a range after %q", ErrSyntax, text, r.text)
		}
		rest = next
	}
	return Constraint{scheme: scheme, alts: alts, text: strings.Join(texts, ",")}, nil
}
