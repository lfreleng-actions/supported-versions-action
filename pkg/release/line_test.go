// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package release

import "testing"

// lineThreeTen is a minor line whose minor has two digits.
const lineThreeTen = "3.10"

// lineThreeNine sorts before lineThreeTen.
const lineThreeNine = "3.9"

func TestValidLine(t *testing.T) {
	tests := []struct {
		line, scheme string
		want         bool
	}{
		{"3", SchemeMajor, true},
		{"0", SchemeMajor, true},
		{"21", SchemeMajor, true},
		{"03", SchemeMajor, false},
		{lineThreeTen, SchemeMajor, false},
		{"", SchemeMajor, false},
		{lineThreeTen, SchemeMajorMinor, true},
		{"0.0", SchemeMajorMinor, true},
		{"3", SchemeMajorMinor, false},
		{"3.010", SchemeMajorMinor, false},
		{"3.10.1", SchemeMajorMinor, false},
		{"v3.10", SchemeMajorMinor, false},
		{"3", "calendar", false},
	}
	for _, tt := range tests {
		if got := ValidLine(tt.line, tt.scheme); got != tt.want {
			t.Errorf("ValidLine(%q, %q) = %v, want %v", tt.line, tt.scheme, got, tt.want)
		}
	}
}

func TestCompareLines(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{lineThreeNine, lineThreeTen, -1},
		{lineThreeTen, lineThreeNine, 1},
		{lineThreeTen, lineThreeTen, 0},
		{"9", "10", -1},
		{"21", "21", 0},
		{"3", "3.0", 0},
		{"2.99", "3.0", -1},
		{"99999999999999999999999", "100000000000000000000000", -1},
		{"1.99999999999999999999999", "1.100000000000000000000000", -1},
	}
	for _, tt := range tests {
		if got := CompareLines(tt.a, tt.b); got != tt.want {
			t.Errorf("CompareLines(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
