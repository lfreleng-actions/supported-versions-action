// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// TestINIValue is the setup.cfg specification: each case gives the
// result setuptools' ConfigParser (optionxform = str) returns for
// get("options", "python_requires"), checked against Python 3.11.
func TestINIValue(t *testing.T) {
	const min312 = ">=3.12"
	const (
		found = iota
		absent
		invalid
	)
	for _, tc := range []struct {
		name, cfg string
		result    int
		want      string
	}{
		{"interpolated from DEFAULT",
			"[DEFAULT]\npython_min = 3.12\n[options]\npython_requires = >=%(python_min)s\n",
			found, min312},
		{"interpolated from the section",
			"[options]\nmin = 3.12\npython_requires = >=%(min)s\n",
			found, min312},
		{"nested interpolation",
			"[DEFAULT]\na = 3.12\nb = %(a)s\n[options]\npython_requires = >=%(b)s\n",
			found, min312},
		{"escaped percent",
			"[options]\npython_requires = >=3.12 %%\n",
			found, ">=3.12 %"},
		{"escaped percent in a nested value",
			"[DEFAULT]\na = 3.12%%\n[options]\npython_requires = >=%(a)s\n",
			found, ">=3.12%"},
		{"missing reference",
			"[options]\npython_requires = >=%(nope)s\n",
			invalid, ""},
		{"bad percent syntax",
			"[options]\npython_requires = >=3.12 %x\n",
			invalid, ""},
		{"percent at the end",
			"[options]\npython_requires = >=3.12%\n",
			invalid, ""},
		{"empty reference",
			"[options]\npython_requires = >=%()s\n",
			invalid, ""},
		{"reference without s",
			"[options]\na = 1\npython_requires = >=%(a)\n",
			invalid, ""},
		{"interpolation too deep",
			"[options]\na = %(b)s\nb = %(a)s\npython_requires = %(a)s\n",
			invalid, ""},
		{"option names are case-sensitive",
			"[options]\nPYTHON_REQUIRES = >=3.12\n",
			absent, ""},
		{"inherited option names are case-sensitive",
			"[DEFAULT]\nPYTHON_REQUIRES = >=3.12\n[options]\nzip_safe = False\n",
			absent, ""},
		{"references are case-sensitive",
			"[DEFAULT]\nMin = 3.12\n[options]\npython_requires = >=%(min)s\n",
			invalid, ""},
		{"section names are case-sensitive",
			"[Options]\npython_requires = >=3.12\n",
			absent, ""},
		{"DEFAULT is case-sensitive",
			"[default]\npython_requires = >=3.12\n[options]\n",
			absent, ""},
		{"inline comment kept",
			"[options]\npython_requires = >=3.12 # note\n",
			found, ">=3.12 # note"},
		{"duplicate option",
			"[options]\npython_requires = >=3.12\npython_requires = >=3.13\n",
			invalid, ""},
		{"duplicate option in another section",
			"[metadata]\nname = a\nname = b\n[options]\npython_requires = >=3.12\n",
			invalid, ""},
		{"duplicate section",
			"[options]\npython_requires = >=3.12\n[options]\nzip_safe = False\n",
			invalid, ""},
		{"DEFAULT blocks with distinct keys merge",
			"[DEFAULT]\na = 3.12\n[options]\n[DEFAULT]\npython_requires = >=%(a)s\n",
			found, min312},
		{"DEFAULT blocks with the same key",
			"[DEFAULT]\npython_requires = >=3.15\n[options]\n[DEFAULT]\npython_requires = >=3.12\n",
			invalid, ""},
		{"explicit empty value overrides DEFAULT",
			"[DEFAULT]\npython_requires = >=3.12\n[options]\npython_requires =\n",
			found, ""},
		{"continuation lines",
			"[options]\npython_requires =\n    >=3.11,\n    <3.15\n",
			found, "\n>=3.11,\n<3.15"},
		{"blank line inside a value",
			"[options]\npython_requires =\n    >=3.12,\n\n    <3.15\n\n",
			found, "\n>=3.12,\n\n<3.15"},
		{"option before any section",
			"[]\npython_requires = >=3.12\n",
			invalid, ""},
		{"line that is not an option",
			"[options]\nfoo\n",
			invalid, ""},
		{"option without a name",
			"[options]\n= >=3.12\n",
			invalid, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := iniValue(tc.cfg, "options", "python_requires")
			switch tc.result {
			case invalid:
				if !errors.Is(err, errINI) {
					t.Errorf("got %q, %v, %v; want an error wrapping errINI", got, ok, err)
				}
			case absent:
				if err != nil || ok {
					t.Errorf("got %q, %v, %v; want no value", got, ok, err)
				}
			default:
				if err != nil || !ok || got != tc.want {
					t.Errorf("got %q, %v, %v; want %q", got, ok, err, tc.want)
				}
			}
		})
	}
}

// nestedRefs returns a setup.cfg whose python_requires expands to
// leaf repeated 10^9 times: each of nine options repeats the previous
// one's reference ten times, within configparser's depth of ten.
func nestedRefs(leaf string) string {
	var b strings.Builder
	b.WriteString("[options]\na0 = " + leaf + "\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "a%d = %s\n", i, strings.Repeat(fmt.Sprintf("%%(a%d)s", i-1), 10))
	}
	b.WriteString("python_requires = %(a9)s\n")
	return b.String()
}

// TestINIInterpolationLimits covers the limits that go beyond
// configparser, which would exhaust memory on these files: an
// interpolated value holds at most maxInterpolatedSize bytes, and one
// lookup makes at most maxInterpolationRefs substitutions.
func TestINIInterpolationLimits(t *testing.T) {
	half := strings.Repeat("x", maxInterpolatedSize/2)
	for _, tc := range []struct {
		name, cfg string
		want      error
	}{
		// Copilot's case: nine levels, each repeating the previous
		// reference ten times. With a 69-byte leaf the output reaches
		// the size limit after about a thousand references.
		{"repeated references, wide leaf",
			nestedRefs(">=3.12" + strings.Repeat(",>=3.12", 9)), errInterpolationSize},
		// The same file with a 6-byte leaf needs more than
		// maxInterpolationRefs references to reach the size limit, so
		// the reference limit stops it first.
		{"repeated references, narrow leaf",
			nestedRefs(">=3.12"), errInterpolationWork},
		// Empty values never grow the output, so only the reference
		// limit stops this: 100 + 100 * 100 substitutions.
		{"references to empty values",
			"[options]\ne =\nx = " + strings.Repeat("%(e)s", 100) +
				"\npython_requires = >=3.12" + strings.Repeat("%(x)s", 100) + "\n",
			errInterpolationWork},
		{"one byte over the size limit",
			"[options]\na = " + half + "\npython_requires = %(a)s%(a)sx\n",
			errInterpolationSize},
		{"one reference over the limit",
			"[options]\ne =\npython_requires = >=3.12" + strings.Repeat("%(e)s", maxInterpolationRefs+1) + "\n",
			errInterpolationWork},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			got, ok, err := iniValue(tc.cfg, "options", "python_requires")
			runtime.ReadMemStats(&after)
			if !errors.Is(err, errINI) || !errors.Is(err, tc.want) {
				t.Fatalf("got %d bytes, %v, %v; want an error wrapping errINI and %v", len(got), ok, err, tc.want)
			}
			if n := after.TotalAlloc - before.TotalAlloc; n > 4<<20 {
				t.Errorf("allocated %d bytes; want at most 4 MiB", n)
			}
		})
	}

	for _, tc := range []struct {
		name, cfg, want string
	}{
		{"nested reference",
			"[DEFAULT]\na = 3.12\nb = %(a)s\n[options]\npython_requires = >=%(b)s\n",
			">=3.12"},
		{"exactly the size limit",
			"[options]\na = " + half + "\npython_requires = %(a)s%(a)s\n",
			half + half},
		{"exactly the reference limit",
			"[options]\ne =\npython_requires = >=3.12" + strings.Repeat("%(e)s", maxInterpolationRefs) + "\n",
			">=3.12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := iniValue(tc.cfg, "options", "python_requires")
			if err != nil || !ok || got != tc.want {
				t.Errorf("got %d bytes, %v, %v; want %d bytes", len(got), ok, err, len(tc.want))
			}
		})
	}
}
