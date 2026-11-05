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

// mavenLines are the Maven lines the maven-range cases are evaluated
// over.
// fromMaven39 is every line from 3.9 up.
const fromMaven39 = "3.9 3.10 4.0"

// maven39 is the Maven 3.9 line.
const maven39 = "3.9"

var mavenLines = strings.Fields("3.6 3.8 3.9 3.10 4.0")

func TestMavenRange(t *testing.T) {
	for _, tc := range []struct {
		text, canonical string // canonical is text when blank
		want            string // the allowed lines, space-separated
	}{
		{"[3.6.3,)", "", "3.6 3.8 3.9 3.10 4.0"},
		{"[3.8,4.0)", "", "3.8 3.9 3.10"},
		{"[3.8, 4.0.0)", "[3.8,4.0.0)", "3.8 3.9 3.10"},
		{"(,3.9]", "", "3.6 3.8 3.9"},
		{"(,3.9)", "", "3.6 3.8"},
		{"(3.9,)", "", fromMaven39},
		{"(3.9.16,)", "", fromMaven39},
		{"[3.9.6]", "", maven39},
		{"[3.9]", "", maven39},
		{"(,3.6],[3.10,)", "", "3.6 3.10 4.0"},
		{" [3.6,3.7) , [4.0,) ", "[3.6,3.7),[4.0,)", "3.6 4.0"},
		{"3.9.6", "", fromMaven39},
		{"3.10", "", "3.10 4.0"},
		{"03.010", "", "3.10 4.0"},
		// Only stable versions count: no stable version of 4.0 is at
		// or below a 4.0.0 pre-release.
		{"[3.9,4.0.0-rc-7]", "", "3.9 3.10"},
		{"[4.0.0-rc-1,)", "", "4.0"},
		{"(4.0.0-alpha-2,)", "", "4.0"},
		{"[3.0-alpha-2,3.9.0-M1)", "", "3.6 3.8"},
		{"[4.0.0-SNAPSHOT]", "", ""},
		// Pre-releases of one version order by qualifier, then by its
		// number, so these ranges are valid and hold no stable version.
		{"[3.9],[4.0.0-rc-1,4.0.0-rc-7)", "", maven39},
		{"[4.0.0-alpha-2,4.0.0-beta-1]", "", ""},
		{"[4.0.0-M1,4.0.0-rc-1)", "", ""},
		{"[4.0.0-rc-7,4.0.0-SNAPSHOT]", "", ""},
		{"[4.0.0-rc-9,4.0.0-rc-10]", "", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			c, err := constraint.Parse(constraint.SyntaxMavenRange, release.SchemeMajorMinor, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.canonical
			if want == "" {
				want = tc.text
			}
			if c.String() != want {
				t.Errorf("String() = %q, want %q", c.String(), want)
			}
			var got []string
			for _, l := range mavenLines {
				if c.Allows(l) {
					got = append(got, l)
				}
			}
			if !slices.Equal(got, strings.Fields(tc.want)) {
				t.Errorf("allowed %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMavenRangeErrors(t *testing.T) {
	for _, text := range []string{
		"",
		"  ",
		"[3.9",
		"3.9]",
		"[3.9,4.0",
		"(3.9)",
		"[3.9)",
		"(,)",
		"[,]",
		"[1,2,3]",
		"[4.0,3.9]",
		"[3.9,3.9)",
		"(3.9,3.9]",
		"[4.0.0-rc-7,4.0.0-rc-1]",
		"[4.0.0-rc-10,4.0.0-rc-9]",
		"[4.0.0-rc-1,4.0.0-alpha-2]",
		"[4.0.0-rc-1,4.0.0-rc-1)",
		"[4.0.0,4.0.0-rc-1]",
		"[3.9,4.0)[4.1,)",
		"[3.9,4.0),",
		"[3.9,4.0),4.1",
		"[[3.9,4.0)",
		"3.9.6.1",
		"3.9.x",
		"latest",
		"3.9.6-foo",
		"[3.9.6-bar,)",
		">=3.9",
		"[1.0.99999999999999999999]",
	} {
		t.Run(text, func(t *testing.T) {
			_, err := constraint.Parse(constraint.SyntaxMavenRange, release.SchemeMajorMinor, text)
			if !errors.Is(err, constraint.ErrSyntax) {
				t.Errorf("Parse(%q) error = %v, want ErrSyntax", text, err)
			}
		})
	}
}

func TestMavenRangeMajorScheme(t *testing.T) {
	c, err := constraint.Parse(constraint.SyntaxMavenRange, release.SchemeMajor, "[3.9,)")
	if err != nil {
		t.Fatal(err)
	}
	for line, want := range map[string]bool{"2": false, "3": true, "4": true} {
		if got := c.Allows(line); got != want {
			t.Errorf("Allows(%q) = %v, want %v", line, got, want)
		}
	}
}

func FuzzMavenRange(f *testing.F) {
	for _, s := range []string{"[1.0,2.0)", "(,1.2]", "[1.0],(2.0,)", "1.0.1", "[2.0.0-rc-1]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		c, err := constraint.Parse(constraint.SyntaxMavenRange, release.SchemeMajorMinor, text)
		if err != nil {
			if !errors.Is(err, constraint.ErrSyntax) {
				t.Fatalf("Parse(%q) error = %v, want ErrSyntax", text, err)
			}
			return
		}
		for _, l := range mavenLines {
			c.Allows(l)
		}
	})
}
