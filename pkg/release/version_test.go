// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package release

import (
	"errors"
	"testing"
)

// spelling is one way to write a version and whether it names a
// pre-release build.
type spelling struct {
	in         string
	prerelease bool
}

func TestParse(t *testing.T) {
	// Spellings from the live feeds on 2026-10-28, grouped by the
	// line they belong to under the scheme their tool uses.
	tests := []struct {
		scheme, line string
		spellings    []spelling
	}{
		// Python.
		{SchemeMajorMinor, "3.15", []spelling{
			{"3.15.2", false},
			{"  3.15.2\n", false},
			{"3.15.0-rc.3", true},
			{"3.15.0-beta.4", true},
			{"3.15.0rc1", true},
			{"3.15.0b2", true},
			{"3.15.0a1", true},
			{"3.15.0.dev1", true},
		}},
		{SchemeMajorMinor, lineThreeTen, []spelling{
			{"py310", false},
			{"PY310", false},
			{"py310+local", false},
		}},
		{SchemeMajorMinor, lineThreeNine, []spelling{
			{"py39", false},
		}},
		// Go.
		{SchemeMajorMinor, "1.27", []spelling{
			{"1.27.3", false},
			{"go1.27.3", false},
			{"1.27rc1", true},
			{"go1.27rc1", true},
			{"1.27beta1", true},
			{"go1.27beta1", true},
			{"1.27alpha1", true},
		}},
		// Node.js.
		{SchemeMajor, "26", []spelling{
			{"26.12.1", false},
		}},
		{SchemeMajor, "22", []spelling{
			{"v22.11.0", false},
			{"V22.11.0", false},
			{"node22.11.0", false},
		}},
		// Temurin.
		{SchemeMajor, "25", []spelling{
			{"25.0.5+8", false},
		}},
		{SchemeMajor, "27", []spelling{
			{"27.0.1.0.2+1", false},
		}},
		{SchemeMajor, "21", []spelling{
			{"temurin-21.0.5+11", false},
			{"21", false},
		}},
		// Maven.
		{SchemeMajorMinor, lineThreeNine, []spelling{
			{"3.9.16", false},
		}},
		{SchemeMajorMinor, "4.0", []spelling{
			{"4.0.0-rc-7", true},
			{"4.0.0-alpha", true},
		}},
		// Gradle.
		{SchemeMajor, "9", []spelling{
			{"9.8.2", false},
			{"9.9.0-milestone-2", true},
			{"9.9.0-20261028014107+0000", true},
		}},
		// Rust.
		{SchemeMajorMinor, "1.99", []spelling{
			{"1.99.1", false},
		}},
	}
	for _, tt := range tests {
		for _, sp := range tt.spellings {
			got, err := Parse(tt.scheme, sp.in)
			if err != nil {
				t.Errorf("Parse(%q, %q) error: %v", tt.scheme, sp.in, err)
				continue
			}
			want := Version{Raw: sp.in, Line: tt.line, Prerelease: sp.prerelease}
			if got != want {
				t.Errorf("Parse(%q, %q) = %+v, want %+v", tt.scheme, sp.in, got, want)
			}
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, scheme, in string
		notAVersion      bool
	}{
		{"missing minor", SchemeMajorMinor, "3", false},
		{"legacy java", SchemeMajor, "1.8.0_412", false},
		{"legacy java, minor scheme", SchemeMajorMinor, "1.8.0_412", false},
		{"unknown hyphen suffix", SchemeMajorMinor, "3.15.0-preview1", false},
		{"unknown hyphen suffix, major", SchemeMajor, "21.0.5-ea", false},
		{"short timestamp", SchemeMajor, "9.9.0-2026102801", false},
		{"trailing dot", SchemeMajor, "21.", false},
		{"leading zero", SchemeMajorMinor, "3.09.1", false},
		{"compact python, leading-zero minor", SchemeMajorMinor, "py300", false},
		{"empty", SchemeMajor, "", false},
		{"blank", SchemeMajor, "   ", false},
		{"unknown scheme", "calendar", "3.15.2", false},
		{"stable", SchemeMajor, "stable", true},
		{"lts glob", SchemeMajor, "lts/*", true},
		{"latest", SchemeMajorMinor, "latest", true},
		{"nightly", SchemeMajorMinor, "nightly", true},
		{"prefix alone", SchemeMajor, "v", true},
	}
	for _, tt := range tests {
		got, err := Parse(tt.scheme, tt.in)
		if err == nil {
			t.Errorf("%s: Parse(%q, %q) = %+v, want an error", tt.name, tt.scheme, tt.in, got)
			continue
		}
		if is := errors.Is(err, ErrNotAVersion); is != tt.notAVersion {
			t.Errorf("%s: Parse(%q, %q) error %q: errors.Is(ErrNotAVersion) = %v, want %v",
				tt.name, tt.scheme, tt.in, err, is, tt.notAVersion)
		}
	}
}
