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

// javaLines are the Temurin lines the Java cases are evaluated over.
var javaLines = []string{"8", "11", "17", "21", "25"}

func javaAllowed(t *testing.T, text string) []string {
	t.Helper()
	c, err := constraint.Parse(constraint.SyntaxJavaRelease, release.SchemeMajor, text)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	var got []string
	for _, l := range javaLines {
		if c.Allows(l) {
			got = append(got, l)
		}
	}
	return got
}

func TestJavaRelease(t *testing.T) {
	// Values from maven.compiler.release, .java-version and .sdkmanrc,
	// with the line each names; the constraint is that line or newer.
	for _, tc := range []struct{ text, line string }{
		{"17", "17"},
		{" 21\n", "21"},
		{"25", "25"},
		{"8", "8"},
		{"1.8", "8"},
		{"1.8.0_412", "8"},
		{"11.0.25", "11"},
		{"21.0.5+11", "21"},
		{"21.0.5-tem", "21"},
		{"17.0.13-zulu", "17"},
		{"21.0.5.fx-zulu", "21"},
		{"17.0.13.crac-librca", "17"},
		{"8.0.432.fx-zulu", "8"},
		{"temurin-21.0.5+11", "21"},
		{"jdk-21", "21"},
		{"JDK-17", "17"},
		{"jdk1.8.0_412", "8"},
		{"27", "27"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			c, err := constraint.Parse(constraint.SyntaxJavaRelease, release.SchemeMajor, tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if want := ">=" + tc.line; c.String() != want {
				t.Errorf("String() = %q, want %q", c.String(), want)
			}
			var want []string
			for _, l := range javaLines {
				if release.CompareLines(l, tc.line) >= 0 {
					want = append(want, l)
				}
			}
			if got := javaAllowed(t, tc.text); !slices.Equal(got, want) {
				t.Errorf("%q allows %v, want %v", tc.text, got, want)
			}
		})
	}
}

func TestJavaReleaseErrors(t *testing.T) {
	for _, text := range []string{
		"", "   ", ">=17", "17+", "017", "0", "1.", "17.", "1.8.0_", "21-ea",
		"21.0.5-", "${java.version}", "latest", "17 21", "17,21", "21.0.5+11-",
		"26.ea-open", "26.ea.1-open", "22.3.r17-grl", "21.fx.1",
	} {
		t.Run(text, func(t *testing.T) {
			_, err := constraint.Parse(constraint.SyntaxJavaRelease, release.SchemeMajor, text)
			if !errors.Is(err, constraint.ErrSyntax) {
				t.Errorf("Parse(%q) error = %v, want ErrSyntax", text, err)
			}
		})
	}
}

func TestJavaReleaseFunc(t *testing.T) {
	for in, want := range map[string]string{"1.8": "8", "1.11": "11", "1": "1", "21.0.5": "21"} {
		got, err := constraint.JavaRelease(in)
		if err != nil || got != want {
			t.Errorf("JavaRelease(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func FuzzJavaRelease(f *testing.F) {
	for _, s := range []string{"17", "1.8.0_412", "21.0.5-tem", "temurin-21.0.5+11"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		c, err := constraint.Parse(constraint.SyntaxJavaRelease, release.SchemeMajor, text)
		if err != nil {
			if !errors.Is(err, constraint.ErrSyntax) {
				t.Fatalf("Parse(%q) error = %v, want ErrSyntax", text, err)
			}
			return
		}
		for _, l := range javaLines {
			c.Allows(l)
		}
	})
}
