// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package constraint_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// rustLines are the lines the semver-min cases are evaluated over.
var rustLines = strings.Fields("1.9 1.10 1.74 1.75 1.99 2.0")

func TestSemverMin(t *testing.T) {
	// Values from Cargo's package.rust-version and a rust-toolchain.toml
	// channel, with the lines each allows.
	for _, tc := range []struct{ text, canonical, allowed string }{
		{"1.74", ">=1.74", "1.74 1.75 1.99 2.0"},
		{"1.74.1", ">=1.74", "1.74 1.75 1.99 2.0"},
		{" 1.75.0\n", ">=1.75", "1.75 1.99 2.0"},
		{"1.10", ">=1.10", "1.10 1.74 1.75 1.99 2.0"},
		{"1", ">=1.0", "1.9 1.10 1.74 1.75 1.99 2.0"},
		{"2", ">=2.0", "2.0"},
		{"1.100", ">=1.100", "2.0"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			c, err := constraint.Parse(constraint.SyntaxSemverMin, release.SchemeMajorMinor, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if c.String() != tc.canonical {
				t.Errorf("String() = %q, want %q", c.String(), tc.canonical)
			}
			got := slices.DeleteFunc(slices.Clone(rustLines), func(l string) bool { return !c.Allows(l) })
			if !slices.Equal(got, strings.Fields(tc.allowed)) {
				t.Errorf("allowed %v, want %s", got, tc.allowed)
			}
		})
	}
}

func TestSemverMinMajorScheme(t *testing.T) {
	c, err := constraint.Parse(constraint.SyntaxSemverMin, release.SchemeMajor, "2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if c.String() != ">=2" || c.Allows("1") || !c.Allows("2") || !c.Allows("3") {
		t.Errorf("constraint %q allows 1 %v, 2 %v, 3 %v", c, c.Allows("1"), c.Allows("2"), c.Allows("3"))
	}
}

func TestSemverMinInvalid(t *testing.T) {
	for _, text := range []string{
		"", "  ", "stable", "nightly-2026-10-01", "^1.74", "~1.74", "<1.74",
		"1.74.0-beta.1", "1.74.0+build", "1.74.0.1", "01.74", "1.074", "v1.74",
		"1.74-x86_64-unknown-linux-gnu", "1.", ".74",
	} {
		t.Run(text, func(t *testing.T) {
			if c, err := constraint.Parse(constraint.SyntaxSemverMin, release.SchemeMajorMinor, text); !errors.Is(err, constraint.ErrSyntax) {
				t.Errorf("Parse(%q) = %q, %v; want ErrSyntax", text, c, err)
			}
		})
	}
}
