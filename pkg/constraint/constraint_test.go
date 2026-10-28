// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package constraint_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

const (
	py310 = "3.10"
	py311 = "3.11"
	py312 = "3.12"
	py313 = "3.13"
	py314 = "3.14"
	py315 = "3.15"
	go126 = "1.26"
	go127 = "1.27"
)

// pyLines are the Python lines the parity cases are evaluated over.
var pyLines = []string{py311, py312, py313, py314, py315}

// goLines are the Go lines the Go cases are evaluated over.
var goLines = []string{go126, go127}

type allowCase struct {
	text string
	want []string
}

func allowed(t *testing.T, syntax, text string, lines []string) []string {
	t.Helper()
	c, err := constraint.Parse(syntax, release.SchemeMajorMinor, text)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	var got []string
	for _, l := range lines {
		if c.Allows(l) {
			got = append(got, l)
		}
	}
	return got
}

func runAllowCases(t *testing.T, syntax string, lines []string, cases []allowCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			got := allowed(t, syntax, tc.text, lines)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%q allows %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func without(lines []string, drop ...string) []string {
	return slices.DeleteFunc(slices.Clone(lines), func(l string) bool {
		return slices.Contains(drop, l)
	})
}

// TestBuildMetadataParity reproduces the build-metadata-action solver's
// results, which the phase 3 replacement must keep.
func TestBuildMetadataParity(t *testing.T) {
	runAllowCases(t, constraint.SyntaxPEP440, pyLines, []allowCase{
		{">=3.10", pyLines},
		{">=3.10,<3.14", []string{py311, py312, py313}},
		{"==3.11", []string{py311}},
		{"!=3.11", without(pyLines, py311)},
		{"~=3.10", pyLines},
		{"^3.10", pyLines},
		{"==3.10.*", nil},
		{">=3.10.1", pyLines},
		{">3.10", pyLines},
		{"<=3.12", []string{py311, py312}},
		{">= 3.10 , < 3.13", []string{py311, py312}},
		{"^3.10.1", pyLines},
		{"~=3.10.1", nil},
		{"<3.13.0", []string{py311, py312}},
		{">=3.10.5,<3.13.0", []string{py311, py312}},
	})
}

// TestPythonSupportedVersionsParity reproduces the fixtures of
// python-supported-versions-action.
func TestPythonSupportedVersionsParity(t *testing.T) {
	runAllowCases(t, constraint.SyntaxPEP440, pyLines, []allowCase{
		{">=3.11", pyLines},
		{"==3.11", []string{py311}},
		{">3.11", []string{py312, py313, py314, py315}},
		{">=3.12", []string{py312, py313, py314, py315}},
		{">=3.11,<3.15", []string{py311, py312, py313, py314}},
		{">=3.11,<=3.13", []string{py311, py312, py313}},
		{"^3.11", pyLines},
		{"==3.12", []string{py312}},
		{"~=3.11.0", []string{py311}},
	})
}

// TestCompatibleReleaseTwoComponents is a deliberate difference from
// python-supported-versions-action, whose Poetry fixture expects "~=X.Y"
// to allow line X.Y alone. PEP 440 defines "~=X.Y" as ">=X.Y, ==X.*",
// which build-metadata-action also follows, so every later line of the
// same major is allowed.
func TestCompatibleReleaseTwoComponents(t *testing.T) {
	runAllowCases(t, constraint.SyntaxPEP440, pyLines, []allowCase{
		{"~=3.11", pyLines},
	})
}

// TestFixes covers where this solver deliberately departs from
// build-metadata-action.
func TestFixes(t *testing.T) {
	runAllowCases(t, constraint.SyntaxPEP440, pyLines, []allowCase{
		{"!=3.13.1", pyLines},
		{"!=3.13.*", without(pyLines, py313)},
		{"!=3.13", without(pyLines, py313)},
		{">=3,<4", pyLines},
		{"<3", nil},
		{">=3.9, !=3.12.*, <4", []string{py311, py313, py314, py315}},
		{"^3.10, !=3.14", without(pyLines, py314)},
		{"^3.11 || ==3.9.*", pyLines},
		{"~3.12", []string{py312}},
		{"~3.12.1", []string{py312}},
		{"~3", pyLines},
		{">=2.7, !=3.0.*, !=3.1.*", pyLines},
		{">=3.11.0rc1", pyLines},
	})
}

// TestPackagingParity follows Python's packaging, which pip uses, on
// the PEP 440 forms it accepts: an optional "v", empty clauses,
// epochs, local versions and arbitrary equality. Release lines carry
// epoch zero and no local part.
func TestPackagingParity(t *testing.T) {
	runAllowCases(t, constraint.SyntaxPEP440, pyLines, []allowCase{
		{">=v3.11", pyLines},
		{">=V3.11", pyLines},
		{">= v3.11", pyLines},
		{"==v3.11.*", []string{py311}},
		{">=3.11,", pyLines},
		{",>=3.11", pyLines},
		{">=3.11,,<3.13", []string{py311, py312}},
		{">=1!3.11", nil},
		{"==1!3.11", nil},
		{"<1!0", pyLines},
		{"!=1!3.11", pyLines},
		{">=0!3.12", []string{py312, py313, py314, py315}},
		{"==3.11+local", nil},
		{"!=3.12+local", pyLines},
		{"===3.11", nil},
		{">=3.11.0.post1", pyLines},
		{"<3.13.0.dev1", []string{py311, py312}},
	})
}

func TestOtherClauses(t *testing.T) {
	runAllowCases(t, constraint.SyntaxPEP440, pyLines, []allowCase{
		{"==3.*", pyLines},
		{"==2.*", nil},
		{"!=3.*", nil},
		{"==3.12.4", []string{py312}},
		{"<3.13.0.dev0", []string{py311, py312}},
		{">=3.11.post1", pyLines},
		{"^0.3", nil},
		{"^3", pyLines},
		{"==3.11 || ==3.13", []string{py311, py313}},
		{"==1!3.*", nil},
		{"<=1!3.11", pyLines},
		{"~=1!3.11", nil},
		{"^1!3.11", nil},
		{"==0!3.11", []string{py311}},
		{"==3.11.0rc1+local", nil},
		{"!=3.12+Loc-al", pyLines},
		{"===", nil},
		{"=== 3.11", nil},
		// PEP 440 makes this "~=3.11"; packaging matches no line for
		// this one spelling, so it is not a parity case.
		{"~=v3.11", pyLines},
	})
}

// TestLeadingZeros covers the leading zeros PEP 440 permits in a
// numeric component: each is normalized away before the comparison,
// and an all-zero component keeps a single zero. As in Python's
// packaging, "<3.011" is "<3.11": it excludes 3.11 as well as 3.12.
func TestLeadingZeros(t *testing.T) {
	runAllowCases(t, constraint.SyntaxPEP440, []string{py310, py311, py312}, []allowCase{
		{"<3.011", []string{py310}},
	})
	runAllowCases(t, constraint.SyntaxPEP440, pyLines, []allowCase{
		{"<3.011", nil},
		{"<3.012", []string{py311}},
		{"==03.*", pyLines},
		{"!=03.*", nil},
		{">=3.0012", []string{py312, py313, py314, py315}},
		{"!=3.013", without(pyLines, py313)},
		{"~=3.011.00", []string{py311}},
		{"^00.3", nil},
		{">=03.00, <04", pyLines},
	})
}

func TestParseErrors(t *testing.T) {
	for _, text := range []string{
		"", "  ", ",", " , ,", "invalid",
		"1!3.11", ">=3.11+local", ">=", ">=3.11 ||", ">=3.11 || ,",
		"=3.11", py311, ">=3.*", "<=3.12.*", "==3.11.*rc1", "~=3", "|| >=3.11",
		">=vv3.11", "==3.11.*+local", "~=3.11+local", "^3.11+local",
		">=3.11+", "==3.11+local+x", "=== 3 .11",
	} {
		t.Run(text, func(t *testing.T) {
			_, err := constraint.Parse(constraint.SyntaxPEP440, release.SchemeMajorMinor, text)
			if !errors.Is(err, constraint.ErrSyntax) {
				t.Errorf("Parse(%q) error = %v, want ErrSyntax", text, err)
			}
		})
	}
}

func TestParseUnknownSyntaxOrScheme(t *testing.T) {
	if _, err := constraint.Parse("maven-range", release.SchemeMajorMinor, "[1,2)"); err == nil {
		t.Error("unsupported syntax: no error")
	}
	if _, err := constraint.Parse(constraint.SyntaxPEP440, "calendar", ">=3"); err == nil {
		t.Error("unknown scheme: no error")
	}
}

func TestGoDirective(t *testing.T) {
	runAllowCases(t, constraint.SyntaxGoDirective, goLines, []allowCase{
		{"1.26.0", goLines},
		{go126, goLines},
		{go127, []string{go127}},
		{"go1.27.3", []string{go127}},
		{"1.27rc1", []string{go127}},
		{"1.27beta1", []string{go127}},
		{"go1.27beta1", []string{go127}},
		{"1.27alpha1", []string{go127}},
		{" go1.27 ", []string{go127}},
	})
	for _, text := range []string{
		"", "banana", "1", "py312", "node1.27.3", "v1.27.3", "1.27.3.4",
		"01.27", "1.027", "1.27-rc1", "1.27rc", "go", "gogo1.27",
		"1.27rc0", "1.27.03", "1.27beta", "1.27foo1", "GO1.27", "1.27RC1",
	} {
		_, err := constraint.Parse(constraint.SyntaxGoDirective, release.SchemeMajorMinor, text)
		if !errors.Is(err, constraint.ErrSyntax) {
			t.Errorf("Parse(%q) error = %v, want ErrSyntax", text, err)
		} else if text != "" && !strings.Contains(err.Error(), strconv.Quote(text)) {
			t.Errorf("Parse(%q) error = %v, want the text quoted", text, err)
		}
	}
}

func TestMajorScheme(t *testing.T) {
	lines := []string{"17", "21", "25"}
	for _, tc := range []allowCase{
		{">=21", []string{"21", "25"}},
		{"!=21.0.1", lines},
		{"!=21", []string{"17", "25"}},
		{"~=21.0", []string{"21"}},
	} {
		c, err := constraint.Parse(constraint.SyntaxPEP440, release.SchemeMajor, tc.text)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.text, err)
		}
		var got []string
		for _, l := range lines {
			if c.Allows(l) {
				got = append(got, l)
			}
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%q allows %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestAllowsMalformedLine(t *testing.T) {
	c, err := constraint.Parse(constraint.SyntaxPEP440, release.SchemeMajorMinor, ">=3.11")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{"", "3", "3.12.1", "x.y"} {
		if c.Allows(l) {
			t.Errorf("Allows(%q) = true", l)
		}
	}
	if (constraint.Constraint{}).Allows(py312) {
		t.Error("zero Constraint allows a line")
	}
}

func TestString(t *testing.T) {
	for text, want := range map[string]string{
		">= 3.10 , < 3.13":     ">=3.10, <3.13",
		">=3.11.0rc1":          ">=3.11.0",
		"^3.11 ||  ==3.9.*":    "^3.11 || ==3.9.*",
		"~=3.10.1, != 3.10.4 ": "~=3.10.1, !=3.10.4",
		"<3.011, ==03.*":       "<3.11, ==3.*",
		">=03.00.000":          ">=3.0.0",
		">= v3.11, , <V3.13":   ">=3.11, <3.13",
		">=01!3.11, >=0!3.12":  ">=1!3.11, >=3.12",
		"!=3.12+Loc-al_1":      "!=3.12+loc.al.1",
		"=== 3.11":             "===3.11",
	} {
		c, err := constraint.Parse(constraint.SyntaxPEP440, release.SchemeMajorMinor, text)
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		if got := c.String(); got != want {
			t.Errorf("Parse(%q).String() = %q, want %q", text, got, want)
		}
	}
	c, err := constraint.Parse(constraint.SyntaxGoDirective, release.SchemeMajorMinor, "go1.27.3")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.String(); got != ">=1.27" {
		t.Errorf("go directive String() = %q", got)
	}
}

func TestNewLineSet(t *testing.T) {
	c, err := constraint.NewLineSet(release.SchemeMajorMinor, []string{py313, py311, py312, py311})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range pyLines {
		if c.Allows(l) {
			got = append(got, l)
		}
	}
	if want := []string{py311, py312, py313}; !slices.Equal(got, want) {
		t.Errorf("line set allows %v, want %v", got, want)
	}
	if got := c.String(); got != "{3.11, 3.12, 3.13}" {
		t.Errorf("String() = %q", got)
	}
	for _, lines := range [][]string{nil, {"3"}, {py311, "3.x"}} {
		if _, err := constraint.NewLineSet(release.SchemeMajorMinor, lines); !errors.Is(err, constraint.ErrSyntax) {
			t.Errorf("NewLineSet(%v) error = %v, want ErrSyntax", lines, err)
		}
	}
	if _, err := constraint.NewLineSet("calendar", []string{py311}); err == nil {
		t.Error("unknown scheme: no error")
	}
}

func TestApply(t *testing.T) {
	matrix := []string{py311, py312, py313}

	got, outcome := constraint.Apply(matrix, nil)
	if outcome != constraint.OutcomeNone || !slices.Equal(got, matrix) {
		t.Errorf("nil: %v %q", got, outcome)
	}

	parse := func(text string) *constraint.Constraint {
		c, err := constraint.Parse(constraint.SyntaxPEP440, release.SchemeMajorMinor, text)
		if err != nil {
			t.Fatal(err)
		}
		return &c
	}

	got, outcome = constraint.Apply(matrix, parse(">=3.12"))
	if outcome != constraint.OutcomeApplied || !slices.Equal(got, []string{py312, py313}) {
		t.Errorf(">=3.12: %v %q", got, outcome)
	}

	got, outcome = constraint.Apply(matrix, parse("<3.0"))
	if outcome != constraint.OutcomeOutOfRangeFallback || !slices.Equal(got, matrix) {
		t.Errorf("<3.0: %v %q", got, outcome)
	}
	got[0] = "changed"
	if matrix[0] != py311 {
		t.Error("Apply returned the caller's slice")
	}
}

func TestOutcomeValues(t *testing.T) {
	got := strings.Join([]string{
		string(constraint.OutcomeNone),
		string(constraint.OutcomeApplied),
		string(constraint.OutcomeOutOfRangeFallback),
	}, " ")
	if want := "none applied out-of-range-fallback"; got != want {
		t.Errorf("outcomes = %q, want %q", got, want)
	}
}
