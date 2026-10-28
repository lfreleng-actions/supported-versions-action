// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package policy_test

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// today is the date the fixture facts were taken from endoflife.date.
const today = "2026-10-28"

const (
	py310 = "3.10"
	py311 = "3.11"
	py312 = "3.12"
	py313 = "3.13"
	py314 = "3.14"
	py315 = "3.15"
	py316 = "3.16"
	go127 = "1.27"
	// pyEOAS is the day 3.10 reached end-of-life and 3.13 left active
	// support.
	pyEOAS = "2026-10-01"
)

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("bad test date %q: %v", s, err)
	}
	return d
}

func datePtr(t *testing.T, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	d := day(t, s)
	return &d
}

func boolPtr(b bool) *bool { return &b }

// fact is a compact form of release.Facts for the fixtures.
type fact struct {
	line, released, eoas, eol, eoes string
	lts                             bool
}

func facts(t *testing.T, fs ...fact) []release.Facts {
	t.Helper()
	out := make([]release.Facts, 0, len(fs))
	for _, f := range fs {
		rf := release.Facts{
			Line:     f.line,
			Released: datePtr(t, f.released),
			EOASFrom: datePtr(t, f.eoas),
			EOLFrom:  datePtr(t, f.eol),
			EOESFrom: datePtr(t, f.eoes),
			LTS:      boolPtr(f.lts),
		}
		out = append(out, rf)
	}
	return out
}

func pythonFacts(t *testing.T) []release.Facts {
	t.Helper()
	return facts(t,
		fact{line: py310, released: "2021-10-04", eol: pyEOAS},
		fact{line: py311, released: "2022-10-24", eol: "2027-10-31"},
		fact{line: py312, released: "2023-10-02", eol: "2028-10-31"},
		fact{line: py313, released: "2024-10-07", eoas: pyEOAS, eol: "2029-10-31"},
		fact{line: py314, released: "2025-10-07", eol: "2030-10-31"},
		fact{line: py315, released: "2026-10-09", eol: "2031-10-31"},
	)
}

func pythonBuilds() map[string]policy.Build {
	return map[string]policy.Build{
		py310: {Stable: "3.10.22"},
		py311: {Stable: "3.11.14"},
		py312: {Stable: "3.12.12"},
		py313: {Stable: "3.13.9"},
		py314: {Stable: "3.14.0"},
		py315: {Stable: "3.15.0"},
	}
}

func defaults(p catalog.Policy) catalog.Policy {
	if p.Admit == "" {
		p.Admit = catalog.DefaultAdmit
	}
	if p.EOLBehaviour == "" {
		p.EOLBehaviour = catalog.DefaultEOLBehaviour
	}
	if p.WarnDaysBeforeEOL == 0 {
		p.WarnDaysBeforeEOL = catalog.DefaultWarnDaysBeforeEOL
	}
	return p
}

func pythonInput(t *testing.T) policy.Input {
	t.Helper()
	return policy.Input{
		Scheme:       release.SchemeMajorMinor,
		Policy:       defaults(catalog.Policy{Rule: catalog.RuleAll, Floor: py311}),
		Facts:        pythonFacts(t),
		Availability: pythonBuilds(),
		Now:          day(t, today),
	}
}

func goInput(t *testing.T) policy.Input {
	t.Helper()
	return policy.Input{
		Scheme: release.SchemeMajorMinor,
		Policy: defaults(catalog.Policy{Rule: catalog.RuleAll}),
		Facts: facts(t,
			fact{line: "1.24", released: "2025-02-11", eol: "2026-02-10"},
			fact{line: "1.25", released: "2025-08-12", eol: "2026-08-19"},
			fact{line: "1.26", released: "2026-02-10"},
			fact{line: go127, released: "2026-08-19"},
		),
		Availability: map[string]policy.Build{
			"1.24": {Stable: "1.24.13"},
			"1.25": {Stable: "1.25.9"},
			"1.26": {Stable: "1.26.4"},
			go127:  {Stable: "1.27.1"},
		},
		Now: day(t, today),
	}
}

func nodeInput(t *testing.T) policy.Input {
	t.Helper()
	return policy.Input{
		Scheme: release.SchemeMajor,
		Policy: defaults(catalog.Policy{Rule: catalog.RuleLTS, Admit: catalog.AdmitReleased}),
		Facts: facts(t,
			fact{line: "20", released: "2023-04-18", eol: "2026-04-30", eoes: "2028-04-30", lts: true},
			fact{line: "22", released: "2024-04-24", eol: "2027-04-30", lts: true},
			fact{line: "24", released: "2025-05-06", eol: "2028-04-30", lts: true},
			fact{line: "25", released: "2025-10-15", eol: "2026-06-01"},
			fact{line: "26", released: "2026-05-05", eol: "2029-04-30", lts: true},
		),
		Now: day(t, today),
	}
}

func javaInput(t *testing.T) policy.Input {
	t.Helper()
	return policy.Input{
		Scheme: release.SchemeMajor,
		Policy: defaults(catalog.Policy{Rule: catalog.RuleLTS, Floor: "17", Admit: catalog.AdmitReleased}),
		Facts: facts(t,
			fact{line: "11", released: "2018-09-25", lts: true},
			fact{line: "17", released: "2021-09-14", lts: true},
			fact{line: "21", released: "2023-09-19", eol: "2029-12-31", lts: true},
			fact{line: "25", released: "2025-09-16", eol: "2031-09-30", lts: true},
			fact{line: "26", released: "2026-03-17", eol: "2026-09-22"},
			fact{line: "27", released: "2026-09-22", eol: "2027-03-23"},
		),
		Now: day(t, today),
	}
}

func rustInput(t *testing.T) policy.Input {
	t.Helper()
	releases := []string{
		"2026-01-22", "2026-03-05", "2026-04-16", "2026-05-28",
		"2026-07-09", "2026-08-20", pyEOAS,
	}
	var fs []fact
	for i, r := range releases {
		f := fact{line: "1.9" + string(rune('3'+i)), released: r}
		if i+1 < len(releases) {
			f.eol = releases[i+1]
		}
		fs = append(fs, f)
	}
	return policy.Input{
		Scheme: release.SchemeMajorMinor,
		Policy: defaults(catalog.Policy{
			Rule: catalog.RuleLatest, Count: 6, Admit: catalog.AdmitReleased,
			EOLBehaviour: catalog.EOLAllow, Labels: []string{"stable"},
		}),
		Facts: facts(t, fs...),
		Now:   day(t, today),
	}
}

func evaluate(t *testing.T, in policy.Input) policy.Result {
	t.Helper()
	res, err := policy.Evaluate(in)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

func lineResult(t *testing.T, res policy.Result, line string) policy.LineResult {
	t.Helper()
	for _, l := range res.Lines {
		if l.Line == line {
			return l
		}
	}
	t.Fatalf("line %s not in result", line)
	return policy.LineResult{}
}

func assertMatrix(t *testing.T, res policy.Result, want ...string) {
	t.Helper()
	if !slices.Equal(res.Matrix, want) {
		t.Errorf("Matrix = %v, want %v", res.Matrix, want)
	}
}

func assertReason(t *testing.T, res policy.Result, line, want string) {
	t.Helper()
	l := lineResult(t, res, line)
	if l.InMatrix || l.Reason != want {
		t.Errorf("line %s: InMatrix=%v Reason=%q, want excluded as %q", line, l.InMatrix, l.Reason, want)
	}
}

func assertNoWarnings(t *testing.T, res policy.Result) {
	t.Helper()
	if len(res.Warnings) != 0 {
		t.Errorf("Warnings = %q, want none", res.Warnings)
	}
}

func TestPython(t *testing.T) {
	res := evaluate(t, pythonInput(t))
	assertMatrix(t, res, py311, py312, py313, py314, py315)
	assertReason(t, res, py310, policy.ReasonBelowFloor)
	if res.Latest != py315 {
		t.Errorf("Latest = %q, want %s", res.Latest, py315)
	}
	assertNoWarnings(t, res)
	l := lineResult(t, res, py313)
	if l.Status != release.StatusSecurity || !l.Installable || l.Newest != "3.13.9" {
		t.Errorf("3.13 = %+v", l)
	}
}

func TestPythonNewLine(t *testing.T) {
	withFacts := func(t *testing.T) policy.Input {
		in := pythonInput(t)
		in.Facts = append(in.Facts, facts(t, fact{line: py316, released: "2027-10-01"})...)
		in.Now = day(t, "2027-10-02")
		return in
	}
	t.Run("facts without a build", func(t *testing.T) {
		res := evaluate(t, withFacts(t))
		assertReason(t, res, py316, policy.ReasonNotInstallable)
	})
	t.Run("facts with a build", func(t *testing.T) {
		in := withFacts(t)
		in.Availability[py316] = policy.Build{Stable: "3.16.0"}
		res := evaluate(t, in)
		if l := lineResult(t, res, py316); !l.InMatrix {
			t.Errorf("3.16 excluded as %q", l.Reason)
		}
		if res.Latest != py316 {
			t.Errorf("Latest = %q, want %s", res.Latest, py316)
		}
	})
	t.Run("build without facts", func(t *testing.T) {
		in := pythonInput(t)
		in.Availability[py316] = policy.Build{Stable: "3.16.0"}
		res := evaluate(t, in)
		assertReason(t, res, py316, policy.ReasonNoFacts)
		if l := lineResult(t, res, py316); l.Status != release.StatusUnknown || !l.Installable {
			t.Errorf("3.16 = %+v", l)
		}
	})
}

func TestPythonFloorAfterEOL(t *testing.T) {
	const eolDay = "2027-11-01"
	in := func(t *testing.T, behavior string) policy.Input {
		in := pythonInput(t)
		in.Now = day(t, eolDay)
		in.Policy.EOLBehaviour = behavior
		return in
	}
	t.Run("warn", func(t *testing.T) {
		res := evaluate(t, in(t, catalog.EOLWarn))
		assertMatrix(t, res, py311, py312, py313, py314, py315)
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], py311) ||
			!strings.Contains(res.Warnings[0], "2027-10-31") {
			t.Errorf("Warnings = %q", res.Warnings)
		}
	})
	t.Run("strip", func(t *testing.T) {
		res := evaluate(t, in(t, catalog.EOLStrip))
		assertMatrix(t, res, py312, py313, py314, py315)
		assertReason(t, res, py311, policy.ReasonEOLStripped)
		if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], py311) {
			t.Errorf("Warnings = %q", res.Warnings)
		}
	})
	t.Run("fail", func(t *testing.T) {
		res, err := policy.Evaluate(in(t, catalog.EOLFail))
		if !errors.Is(err, policy.ErrEOL) || !strings.Contains(err.Error(), py311) {
			t.Fatalf("err = %v, want ErrEOL naming %s", err, py311)
		}
		assertMatrix(t, res, py311, py312, py313, py314, py315)
		if len(res.Lines) != 6 {
			t.Errorf("Lines has %d entries, want 6", len(res.Lines))
		}
	})
}

func TestAdvanceWarning(t *testing.T) {
	in := pythonInput(t)
	in.Now = day(t, "2027-08-15")
	res := evaluate(t, in)
	want := []string{"line 3.11 reaches end-of-life on 2027-10-31, in 77 days"}
	if !slices.Equal(res.Warnings, want) {
		t.Errorf("Warnings = %q, want %q", res.Warnings, want)
	}
	if d := lineResult(t, res, py311).DaysToEOL; d == nil || *d != 77 {
		t.Errorf("DaysToEOL = %v, want 77", d)
	}

	in.Policy.WarnDaysBeforeEOL = 0
	assertNoWarnings(t, evaluate(t, in))
}

func TestGo(t *testing.T) {
	res := evaluate(t, goInput(t))
	assertMatrix(t, res, "1.26", go127)
	assertReason(t, res, "1.24", policy.ReasonEOL)
	assertReason(t, res, "1.25", policy.ReasonEOL)
	assertNoWarnings(t, res)
	if res.Latest != go127 {
		t.Errorf("Latest = %q", res.Latest)
	}
}

func TestNode(t *testing.T) {
	for _, current := range []bool{false, true} {
		in := nodeInput(t)
		in.Policy.Current = current
		res := evaluate(t, in)
		assertMatrix(t, res, "22", "24", "26")
		assertReason(t, res, "20", policy.ReasonEOL)
		assertReason(t, res, "25", policy.ReasonNotLTS)
		if s := lineResult(t, res, "20").Status; s != release.StatusExtended {
			t.Errorf("20 status = %q, want extended", s)
		}
		assertNoWarnings(t, res)
	}
}

func TestJava(t *testing.T) {
	res := evaluate(t, javaInput(t))
	assertMatrix(t, res, "17", "21", "25")
	assertReason(t, res, "11", policy.ReasonBelowFloor)
	assertReason(t, res, "26", policy.ReasonNotLTS)
	assertReason(t, res, "27", policy.ReasonNotLTS)

	in := javaInput(t)
	in.Policy.Current = true
	res = evaluate(t, in)
	assertMatrix(t, res, "17", "21", "25", "27")
	assertReason(t, res, "26", policy.ReasonNotLTS)
	if res.Latest != "27" {
		t.Errorf("Latest = %q, want 27", res.Latest)
	}
}

func TestRust(t *testing.T) {
	res := evaluate(t, rustInput(t))
	assertMatrix(t, res, "1.94", "1.95", "1.96", "1.97", "1.98", "1.99")
	assertReason(t, res, "1.93", policy.ReasonNotLatest)
	assertNoWarnings(t, res)
	if !slices.Equal(res.Labels, []string{"stable"}) {
		t.Errorf("Labels = %v", res.Labels)
	}
	if res.Latest != "1.99" {
		t.Errorf("Latest = %q", res.Latest)
	}
}

func TestRustPrereleaseOutsideCount(t *testing.T) {
	in := rustInput(t)
	in.Policy.Prerelease = true
	in.Facts = append(in.Facts, facts(t, fact{line: "1.100", released: "2026-11-12"})...)
	res := evaluate(t, in)
	assertMatrix(t, res, "1.94", "1.95", "1.96", "1.97", "1.98", "1.99", "1.100")
	if res.Latest != "1.99" {
		t.Errorf("Latest = %q, want 1.99", res.Latest)
	}
}

func TestPrerelease(t *testing.T) {
	in := func(t *testing.T, admit bool) policy.Input {
		in := pythonInput(t)
		in.Policy.Prerelease = admit
		in.Availability[py316] = policy.Build{Prerelease: "3.16.0a1"}
		return in
	}
	res := evaluate(t, in(t, false))
	assertReason(t, res, py316, policy.ReasonPrerelease)
	if l := lineResult(t, res, py316); l.Status != release.StatusPrerelease || l.Newest != "3.16.0a1" {
		t.Errorf("3.16 = %+v", l)
	}

	res = evaluate(t, in(t, true))
	assertMatrix(t, res, py311, py312, py313, py314, py315, py316)
	if res.Latest != py315 {
		t.Errorf("Latest = %q, want %s", res.Latest, py315)
	}
}

func TestPrereleaseFactsWithoutBuild(t *testing.T) {
	in := pythonInput(t)
	in.Policy.Prerelease = true
	in.Facts = append(in.Facts, facts(t, fact{line: py316, released: "2027-10-01"})...)
	assertReason(t, evaluate(t, in), py316, policy.ReasonPrerelease)
}

func TestGrace(t *testing.T) {
	in := pythonInput(t)
	in.Policy.GraceDays = 30
	res := evaluate(t, in)
	assertReason(t, res, py315, policy.ReasonGrace)
	if res.Latest != py314 {
		t.Errorf("Latest = %q, want 3.14", res.Latest)
	}

	in.Now = day(t, "2026-11-09")
	if l := lineResult(t, evaluate(t, in), py315); !l.InMatrix {
		t.Errorf("3.15 excluded as %q on 2026-11-09", l.Reason)
	}
}

func TestExplicitCeiling(t *testing.T) {
	in := pythonInput(t)
	in.Policy.Admit = catalog.AdmitExplicit
	in.Policy.Ceiling = py314
	res := evaluate(t, in)
	assertMatrix(t, res, py311, py312, py313, py314)
	assertReason(t, res, py315, policy.ReasonAboveCeiling)
}

func TestNoAvailability(t *testing.T) {
	in := pythonInput(t)
	in.Availability = nil
	res := evaluate(t, in)
	assertMatrix(t, res, py311, py312, py313, py314, py315)
	if !slices.Equal(res.Warnings, []string{policy.WarnNoAvailability}) {
		t.Errorf("Warnings = %q", res.Warnings)
	}
	if lineResult(t, res, py311).Installable {
		t.Error("3.11 Installable with no availability")
	}
}

func TestCheck(t *testing.T) {
	python := evaluate(t, pythonInput(t))
	java := evaluate(t, javaInput(t))
	rust := evaluate(t, rustInput(t))
	cases := map[string]struct {
		res            policy.Result
		scheme         string
		version        string
		line           string
		status         release.Status
		inMatrix       bool
		supported      bool
		policyExcluded bool
		reason         string
	}{
		"python eol": {
			res: python, scheme: release.SchemeMajorMinor, version: "3.10.22",
			line: py310, status: release.StatusEOL, reason: policy.ReasonBelowFloor,
		},
		"python supported": {
			res: python, scheme: release.SchemeMajorMinor, version: "3.13.4",
			line: py313, status: release.StatusSecurity, inMatrix: true, supported: true,
		},
		"java policy excluded": {
			res: java, scheme: release.SchemeMajor, version: "11.0.20+8",
			line: "11", status: release.StatusActive, policyExcluded: true, reason: policy.ReasonBelowFloor,
		},
		"rust allowed eol": {
			res: rust, scheme: release.SchemeMajorMinor, version: "1.95.0",
			line: "1.95", status: release.StatusEOL, inMatrix: true,
		},
		"unknown line": {
			res: python, scheme: release.SchemeMajorMinor, version: "3.99.1",
			line: "3.99", status: release.StatusUnknown,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := policy.Check(tc.res, tc.scheme, tc.version)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.Line != tc.line || got.Status != tc.status || got.InMatrix != tc.inMatrix ||
				got.Supported != tc.supported || got.PolicyExcluded != tc.policyExcluded ||
				got.Reason != tc.reason {
				t.Errorf("Check(%q) = %+v", tc.version, got)
			}
		})
	}

	got, err := policy.Check(python, release.SchemeMajorMinor, "3.13.4")
	if err != nil || got.EOL == nil || !got.EOL.Equal(day(t, "2029-10-31")) || got.DaysToEOL == nil {
		t.Errorf("Check 3.13.4 EOL = %v, DaysToEOL = %v, err = %v", got.EOL, got.DaysToEOL, err)
	}

	if _, err := policy.Check(rust, release.SchemeMajorMinor, "stable"); !errors.Is(err, release.ErrNotAVersion) {
		t.Errorf("Check(stable) err = %v, want ErrNotAVersion", err)
	}
}

func TestDeterministic(t *testing.T) {
	for name, build := range map[string]func(*testing.T) policy.Input{
		"python": pythonInput, "go": goInput, "node": nodeInput, "java": javaInput, "rust": rustInput,
	} {
		t.Run(name, func(t *testing.T) {
			want := evaluate(t, build(t))
			rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // test shuffling only
			for range 20 {
				in := build(t)
				rng.Shuffle(len(in.Facts), func(i, j int) {
					in.Facts[i], in.Facts[j] = in.Facts[j], in.Facts[i]
				})
				if got := evaluate(t, in); !reflect.DeepEqual(got, want) {
					t.Fatalf("shuffled facts changed the result:\n got %+v\nwant %+v", got, want)
				}
			}
		})
	}
}

func TestInvalidInput(t *testing.T) {
	cases := map[string]func(*policy.Input){
		"unknown scheme":       func(in *policy.Input) { in.Scheme = "semver" },
		"facts line mismatch":  func(in *policy.Input) { in.Facts[0].Line = "3" },
		"duplicate facts line": func(in *policy.Input) { in.Facts[1].Line = in.Facts[0].Line },
		"availability line":    func(in *policy.Input) { in.Availability["3"] = policy.Build{} },
		"unknown rule":         func(in *policy.Input) { in.Policy.Rule = "some" },
		"latest without count": func(in *policy.Input) { in.Policy.Rule = catalog.RuleLatest },
		"unknown admit":        func(in *policy.Input) { in.Policy.Admit = "always" },
		"unknown behavior":     func(in *policy.Input) { in.Policy.EOLBehaviour = "ignore" },
		"bad floor":            func(in *policy.Input) { in.Policy.Floor = "3" },
		"bad ceiling":          func(in *policy.Input) { in.Policy.Ceiling = "x" },
		"negative grace":       func(in *policy.Input) { in.Policy.GraceDays = -1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := pythonInput(t)
			mutate(&in)
			if _, err := policy.Evaluate(in); err == nil {
				t.Error("Evaluate accepted invalid input")
			}
		})
	}
}
