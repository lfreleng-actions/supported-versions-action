// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package release turns version strings into the release lines that
// support applies to, and decides a line's status from dated release
// facts.
//
// A version is what a user, lock file or feed names, such as a full
// patch release with a prefix or build metadata. A release line is what
// support covers: one number under SchemeMajor, two under
// SchemeMajorMinor. This package holds the module's only line patterns
// and line comparison, and depends on the standard library alone.
package release

import (
	"cmp"
	"regexp"
	"strings"
)

// Version schemes.
const (
	// SchemeMajor lines are a single number.
	SchemeMajor = "major"
	// SchemeMajorMinor lines are two dot-separated numbers.
	SchemeMajorMinor = "major.minor"
)

var linePatterns = map[string]*regexp.Regexp{
	SchemeMajor:      regexp.MustCompile(`^(0|[1-9][0-9]*)$`),
	SchemeMajorMinor: regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`),
}

// ValidLine reports whether line is a well-formed release line under
// scheme. It is false for an unknown scheme.
func ValidLine(line, scheme string) bool {
	re, ok := linePatterns[scheme]
	return ok && re.MatchString(line)
}

// CompareLines compares two well-formed release lines numerically, so
// that a minor of 9 sorts before a minor of 10, and returns -1, 0 or
// +1. A missing component counts as zero. Components carry no leading
// zeros, so comparing their digit strings by length and then lexically
// orders them numerically at any size, with no overflow.
func CompareLines(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := "0", "0"
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if c := cmp.Compare(len(x), len(y)); c != 0 {
			return c
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}
