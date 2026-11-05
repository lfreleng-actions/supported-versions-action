// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package constraint_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

const (
	// eslintEngines is eslint 9's engines.node.
	eslintEngines = "^18.18.0 || ^20.9.0 || >=21.1.0"
	hyphenEngines = "18 - 22"
)

// nodeLines are the Node.js lines the semver-range cases are
// evaluated over, odd and even.
var nodeLines = []string{"18", "19", "20", "21", "22", "23", "24", "25", "26"}

func nodeAllowed(t *testing.T, text string) []string {
	t.Helper()
	c, err := constraint.Parse(constraint.SyntaxSemverRange, release.SchemeMajor, text)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	var got []string
	for _, l := range nodeLines {
		if c.Allows(l) {
			got = append(got, l)
		}
	}
	return got
}

func TestSemverRangeEngines(t *testing.T) {
	// engines.node values published by real packages, and the forms
	// the node-semver grammar documents.
	for _, tc := range []allowCase{
		// Common floors.
		{">=18", nodeLines},
		{">=18.0.0", nodeLines},
		{">=14.17", nodeLines},
		{">= 20", nodeLines[2:]},
		{">=20.19.0", nodeLines[2:]},
		// eslint 9 and typescript-eslint.
		{eslintEngines, without(nodeLines, "19")},
		// vite 6.
		{"^18.0.0 || ^20.0.0 || >=22.0.0", without(nodeLines, "19", "21")},
		// vite 7.
		{"^20.19.0 || >=22.12.0", without(nodeLines, "18", "19", "21")},
		// next 15.
		{"^18.18.0 || ^19.8.0 || >= 20.0.0", nodeLines},
		// x-ranges.
		{"18.x || 20.x || 22.x", []string{"18", "20", "22"}},
		{"20.*", []string{"20"}},
		{"22", []string{"22"}},
		{"*", nodeLines},
		{"x", nodeLines},
		// Hyphen ranges.
		{hyphenEngines, nodeLines[:5]},
		{"18.0.0 - 22.0.0", nodeLines[:5]},
		{"20.1 - 22.x", []string{"20", "21", "22"}},
		// Exact versions.
		{"=20.1.0", []string{"20"}},
		{"20.1.0", []string{"20"}},
		{"v22.11.0", []string{"22"}},
		// Caret and tilde.
		{"^22", []string{"22"}},
		{"^22.11.0", []string{"22"}},
		{"~20.11.0", []string{"20"}},
		{"~22", []string{"22"}},
		{"~> 22.1", []string{"22"}},
		// Comparators and intersections.
		{">18", nodeLines[1:]},
		{">18.0.0", nodeLines},
		{"<22", nodeLines[:4]},
		{"<22.1.0", nodeLines[:5]},
		{"<=22", nodeLines[:5]},
		{"<=22.0.0", nodeLines[:5]},
		{"<23 >=18", nodeLines[:5]},
		{">=20.0.0 <21.0.0", []string{"20"}},
		{">= 18 < 20", []string{"18", "19"}},
		// An empty alternative allows every version.
		{"22 ||", nodeLines},
		// Versions are discrete: no stable version lies between
		// adjacent patches, nor between a version and its prerelease.
		{">22.0.0 <22.0.1 || 24", []string{"24"}},
		{">22.0.0 <=22.0.1", []string{"22"}},
		{">=22.0.0 <=22.0.0-rc.1 || 24", []string{"24"}},
		{">=22.0.0 <22.0.0-rc.1 || 24", []string{"24"}},
		{">22.0.0-rc.1 <22.0.1", []string{"22"}},
		{">=22.0.0-rc.1 <22.0.0 || 24", []string{"24"}},
		{"22.0.0-rc.1 || 24", []string{"24"}},
		{"<=22.0.0-rc.1", nodeLines[:4]},
		{"<22.0.0-rc.1", nodeLines[:4]},
		{"^22.0.0-rc.1", []string{"22"}},
		{"~22.1.0-beta", []string{"22"}},
		{"18 - 22.0.0-rc.1", nodeLines[:4]},
		{"18.0.0-rc.1 - 18.0.0-rc.2 || 24", []string{"24"}},
		{"21.0.0-rc.1 - 22", []string{"21", "22"}},
		// node-semver ignores a prerelease on a wildcard version.
		{"22.x.x-rc.1 || 24", []string{"22", "24"}},
		{"18 - 22.x.x-rc.1", nodeLines[:5]},
		{">22.x.x-rc.1", nodeLines[5:]},
		{"<=22.x.x-rc.1", nodeLines[:5]},
		// node-semver strips build metadata from each comparator,
		// partial versions included, and from a full hyphen range end.
		{">=22.x+build <25.x+build", []string{"22", "23", "24"}},
		{"22+b || 24.x+b.1", []string{"22", "24"}},
		{">=22.0.0-rc+b <24", []string{"22", "23"}},
		{">=22.0.0-rc.1+001 <24", []string{"22", "23"}},
		{"22.0.0+b+c", []string{"22"}},
		{"*+b", nodeLines},
		{"18 - 22.0.0+b", nodeLines[:5]},
		// A prerelease identifier may lead with a zero when it is not
		// a number.
		{"^22.0.0-0a || 24", []string{"22", "24"}},
		// node-semver drops the components after a wildcard.
		{"x.22", nodeLines},
		{"22.x.5", []string{"22"}},
		// Nothing satisfies these.
		{">=18.17.0 <18.10.0", nil},
		{">=24 <22", nil},
		{"<18 >26", nil},
		{">*", nil},
		{"<0.0.0", nil},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := nodeAllowed(t, tc.text); !slices.Equal(got, tc.want) {
				t.Errorf("%q allows %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestSemverRangeZeroMajor(t *testing.T) {
	lines := []string{"0", "1", "2"}
	for _, tc := range []allowCase{
		{"^0.2.3", []string{"0"}},
		{"^0.x", []string{"0"}},
		{"^0.0", []string{"0"}},
		{"~0", []string{"0"}},
		{"^1.x", []string{"1"}},
		{">0", []string{"1", "2"}},
	} {
		c, err := constraint.Parse(constraint.SyntaxSemverRange, release.SchemeMajor, tc.text)
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

func TestSemverRangeMajorMinor(t *testing.T) {
	lines := []string{"1.25", go126, go127}
	runAllowCases(t, constraint.SyntaxSemverRange, lines, []allowCase{
		{"^1.26", []string{go126, go127}},
		{"~1.26.3", []string{go126}},
		{">1.26", []string{go127}},
		{"1.25 - 1.26", []string{"1.25", go126}},
	})
}

func TestSemverRangeErrors(t *testing.T) {
	for _, text := range []string{
		"", "   ", "banana", ">=", "18 -", "- 22", "lts/*", ">=18,<20",
		"1.2.3.4", "018", ">=18 - 22", "18 - 22 - 24", "18 - >=22",
		"<=99999999999999999999", "^18 ||| 20", "node",
		// Numeric prerelease identifiers take no leading zero.
		"^22.0.0-01 || 24", ">=22.0.0-rc.01 <24", "22.0.0-00 || 24",
		"22.0.0-01 - 24", "18 - 22.0.0-rc.01",
		// Build metadata on a partial hyphen range end, and a second
		// build on a comparator with no patch.
		"18 - 22.x+b", "22+b - 24", "22+b+c",
	} {
		_, err := constraint.Parse(constraint.SyntaxSemverRange, release.SchemeMajor, text)
		if !errors.Is(err, constraint.ErrSyntax) {
			t.Errorf("Parse(%q) error = %v, want ErrSyntax", text, err)
		}
	}
}

// TestSemverRangeOperatorLeads pins node-semver 7.7.4's handling of
// "v" and "=" after an operator: any run of them before a partial,
// caret or tilde version, but at most one "v" before a full version
// read as written. A nil want with invalid set expects ErrSyntax.
func TestSemverRangeOperatorLeads(t *testing.T) {
	for _, tc := range []struct {
		text    string
		want    []string
		invalid bool
	}{
		// A doubled "=" before a full version, prerelease or not.
		{text: "==22.0.0", invalid: true},
		{text: ">==22.0.0", invalid: true},
		{text: "==22.0.0-rc.1", invalid: true},
		{text: "== 22.0.0", invalid: true},
		{text: "==22.0.0+b", invalid: true},
		{text: ">==22.0.0+b", invalid: true},
		{text: "v=22.0.0", invalid: true},
		{text: "=v=22.0.0", invalid: true},
		// A doubled "=" before a partial or wildcard version.
		{text: "==22", want: []string{"22"}},
		{text: "==22.0", want: []string{"22"}},
		{text: "==22.x", want: []string{"22"}},
		{text: ">==22", want: nodeLines[4:]},
		{text: ">==22.0", want: nodeLines[4:]},
		{text: ">==22.x.x", want: nodeLines[4:]},
		{text: "<==22.0", want: nodeLines[:5]},
		{text: "==22.x.x-rc.1", want: []string{"22"}},
		{text: "==22+b", want: []string{"22"}},
		{text: "==*", want: nodeLines},
		{text: ">==*", want: nodeLines},
		{text: "vv22", want: []string{"22"}},
		{text: "v=22", want: []string{"22"}},
		{text: "> =22", want: nodeLines[4:]},
		{text: ">= =22", want: nodeLines[4:]},
		{text: "~ =22", want: []string{"22"}},
		// Whitespace joins an operator only straight after it.
		{text: "== 22", invalid: true},
		{text: ">== 22", invalid: true},
		{text: "<== 22.x", invalid: true},
		{text: ">=v 22", invalid: true},
		// Operators that are not node-semver's.
		{text: "=>22.0.0", invalid: true},
		{text: "<>22.0", invalid: true},
		{text: ">>22", invalid: true},
		{text: "=<22", invalid: true},
		{text: "~^22.0.0", invalid: true},
		{text: "^~22", invalid: true},
		// Valid operators, with and without a space after them.
		{text: "<22", want: nodeLines[:4]},
		{text: "< 22", want: nodeLines[:4]},
		{text: "<=22", want: nodeLines[:5]},
		{text: "<= 22", want: nodeLines[:5]},
		{text: ">22", want: nodeLines[5:]},
		{text: "> 22", want: nodeLines[5:]},
		{text: ">=22", want: nodeLines[4:]},
		{text: ">= 22", want: nodeLines[4:]},
		{text: "=22", want: []string{"22"}},
		{text: "= 22", want: []string{"22"}},
		{text: "22", want: []string{"22"}},
		{text: "^22", want: []string{"22"}},
		{text: "^ 22", want: []string{"22"}},
		{text: "~22", want: []string{"22"}},
		{text: "~ 22", want: []string{"22"}},
		{text: "~>22", want: []string{"22"}},
		{text: "~> 22", want: []string{"22"}},
		// One "v" before a full version, after a single operator.
		{text: "=v22.0.0", want: []string{"22"}},
		{text: "= 22.0.0", want: []string{"22"}},
		{text: "<=v22.0.0", want: nodeLines[:5]},
		{text: ">=v22.0.0", want: nodeLines[4:]},
		// Caret and tilde rebuild a full version, so any lead goes.
		{text: "^==22.0.0", want: []string{"22"}},
		{text: "~==22.0.0", want: []string{"22"}},
		{text: "~>=22.0.0", want: []string{"22"}},
		{text: "^=v=22.1.0", want: []string{"22"}},
		// Hyphen ends: a full end stays as written, but a partial end
		// and a prerelease upper end are rebuilt.
		{text: "=22.0.0 - 24", invalid: true},
		{text: "=22.0.0-rc.1 - 24", invalid: true},
		{text: "18 - =22.0.0", invalid: true},
		{text: "v22.0.0 - 24", want: []string{"22", "23", "24"}},
		{text: "==22 - 24", want: []string{"22", "23", "24"}},
		{text: "18 - v22.0.0", want: nodeLines[:5]},
		{text: "18 - ==22", want: nodeLines[:5]},
		{text: "18 - =22.0.0-rc.1", want: nodeLines[:4]},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if tc.invalid {
				_, err := constraint.Parse(constraint.SyntaxSemverRange, release.SchemeMajor, tc.text)
				if !errors.Is(err, constraint.ErrSyntax) {
					t.Errorf("Parse(%q) error = %v, want ErrSyntax", tc.text, err)
				}
				return
			}
			if got := nodeAllowed(t, tc.text); !slices.Equal(got, tc.want) {
				t.Errorf("%q allows %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestSemverRangeString(t *testing.T) {
	for in, want := range map[string]string{
		eslintEngines:  eslintEngines,
		">= 18   < 23": ">=18 <23",
		"  18 - 22 ":   hyphenEngines,
		"20 ||":        "20 || *",
	} {
		c, err := constraint.Parse(constraint.SyntaxSemverRange, release.SchemeMajor, in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got := c.String(); got != want {
			t.Errorf("Parse(%q).String() = %q, want %q", in, got, want)
		}
	}
}

func FuzzSemverRange(f *testing.F) {
	for _, s := range []string{eslintEngines, hyphenEngines, ">= 18 <23", "20.x", "*", "~> 22.1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		c, err := constraint.Parse(constraint.SyntaxSemverRange, release.SchemeMajor, text)
		if err != nil {
			if !errors.Is(err, constraint.ErrSyntax) {
				t.Fatalf("Parse(%q) error = %v, want ErrSyntax", text, err)
			}
			return
		}
		for _, l := range nodeLines {
			c.Allows(l)
		}
	})
}
