// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package release

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrNotAVersion reports input that is not a version at all, such as a
// channel label like "stable" or "latest". Callers test for it with
// errors.Is to tell such labels apart from malformed versions.
var ErrNotAVersion = errors.New("not a version")

// Version is a parsed version string and the release line it belongs
// to.
type Version struct {
	// Raw is the input exactly as given.
	Raw string
	// Line is the release line under the scheme used to parse Raw.
	Line string
	// Prerelease is true when Raw names a pre-release build.
	Prerelease bool
}

// prefixes are stripped, case-insensitively, from the start of a
// version; at most one is removed.
var prefixes = []string{"temurin-", "node", "go", "v"}

var (
	numericPart = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)*)(.*)$`)
	// compactPython is the "py" form: the first digit is the major
	// and the rest is the minor.
	compactPython = regexp.MustCompile(`^(?i:py)([0-9])([0-9]+)$`)
	// prereleaseSuffixes are the suffixes, after the numeric
	// components, that mark a pre-release build.
	prereleaseSuffixes = []*regexp.Regexp{
		// Hyphenated labels, optionally numbered: -rc, -rc.3, -rc-7.
		regexp.MustCompile(`^(?i)-(?:rc|beta|alpha|milestone)(?:[.-]?[0-9]+)?$`),
		// PEP 440 and Go labels directly after the numbers: rc1, b2, a1.
		regexp.MustCompile(`^(?i)(?:a|b|rc)[0-9]*$`),
		// PEP 440 development releases: .dev1.
		regexp.MustCompile(`^(?i)\.dev[0-9]*$`),
		// Gradle nightlies: a hyphen and a 14-digit timestamp.
		regexp.MustCompile(`^-[0-9]{14}$`),
	}
)

// Parse reads the version s and returns the release line it belongs to
// under scheme.
//
// Surrounding whitespace and one leading prefix ("v", "go", "node" or
// "temurin-", in any case) are dropped, as is build metadata after a
// "+". The compact Python form "py" plus digits reads its first digit
// as the major and the rest as the minor. The leading dot-separated
// numbers are then cut to the scheme's line. A known pre-release
// suffix sets Prerelease; any other suffix is an error.
//
// Input that does not start with a number once any prefix is dropped
// returns an error wrapping ErrNotAVersion. Input containing "_" is
// rejected, so tool-specific legacy forms never turn into a wrong line.
func Parse(scheme, s string) (Version, error) {
	v := Version{Raw: s}
	if _, ok := linePatterns[scheme]; !ok {
		return v, fmt.Errorf("unknown version scheme %q", scheme)
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return v, errors.New("empty version")
	}
	if strings.Contains(t, "_") {
		return v, fmt.Errorf("version %q: unsupported form containing %q", s, "_")
	}

	t, _, _ = strings.Cut(t, "+")

	var parts []string
	if m := compactPython.FindStringSubmatch(t); m != nil {
		parts = []string{m[1], m[2]}
	} else {
		lower := strings.ToLower(t)
		for _, p := range prefixes {
			if strings.HasPrefix(lower, p) {
				t = t[len(p):]
				break
			}
		}
		m := numericPart.FindStringSubmatch(t)
		if m == nil {
			return v, fmt.Errorf("version %q: %w", s, ErrNotAVersion)
		}
		if m[2] != "" {
			if !isPrereleaseSuffix(m[2]) {
				return v, fmt.Errorf("version %q: unknown suffix %q", s, m[2])
			}
			v.Prerelease = true
		}
		parts = strings.Split(m[1], ".")
	}

	n := 1
	if scheme == SchemeMajorMinor {
		n = 2
	}
	if len(parts) < n {
		return v, fmt.Errorf("version %q: too few components for a %s line", s, scheme)
	}
	line := strings.Join(parts[:n], ".")
	if !ValidLine(line, scheme) {
		return v, fmt.Errorf("version %q: %q is not a %s line", s, line, scheme)
	}
	v.Line = line
	return v, nil
}

func isPrereleaseSuffix(suffix string) bool {
	for _, re := range prereleaseSuffixes {
		if re.MatchString(suffix) {
			return true
		}
	}
	return false
}
