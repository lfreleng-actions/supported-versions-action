// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package policy decides which release lines of a tool make up the
// supported set, the matrix, and why every other line is excluded.
//
// Evaluate is a pure function of its Input: it reads no files and makes
// no requests. A resolver gathers the release facts and the builds the
// setup actions can install and passes them in. Check then answers
// whether one version is supported under the Result.
package policy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// ErrEOL reports that an evaluation under the "fail" end-of-life
// behavior named one or more end-of-life lines.
var ErrEOL = errors.New("end-of-life lines in the supported set")

// Reasons a line is excluded from the matrix.
const (
	// ReasonPrerelease is a pre-release line the policy does not admit.
	ReasonPrerelease = "prerelease"
	// ReasonNoFacts is a line no release facts list.
	ReasonNoFacts = "no-facts"
	// ReasonBelowFloor is a line below Policy.Floor.
	ReasonBelowFloor = "below-floor"
	// ReasonAboveCeiling is a line above Policy.Ceiling.
	ReasonAboveCeiling = "above-ceiling"
	// ReasonNotInstallable is a line with no stable build to install.
	ReasonNotInstallable = "not-installable"
	// ReasonGrace is a line still within Policy.GraceDays of release.
	ReasonGrace = "grace"
	// ReasonNotLTS is a line the "lts" rule does not keep.
	ReasonNotLTS = "not-lts"
	// ReasonNotLatest is a line outside the newest Policy.Count lines.
	ReasonNotLatest = "not-latest"
	// ReasonEOL is an end-of-life line the policy does not name.
	ReasonEOL = "eol"
	// ReasonEOLStripped is an end-of-life line the policy names that
	// the "strip" behavior removes.
	ReasonEOLStripped = "eol-stripped"
)

// WarnNoAvailability is the warning Evaluate raises for "installable"
// admission when the tool has no installability source.
const WarnNoAvailability = "no installability source for this tool; admitting released lines"

// Build is what the setup actions can install for one release line.
type Build struct {
	// Stable is the newest stable build of the line, "" if none.
	Stable string
	// Prerelease is the newest pre-release build of the line, "" if
	// none.
	Prerelease string
}

// Input is everything Evaluate decides from.
type Input struct {
	// Scheme is the tool's version scheme.
	Scheme string
	// Policy is the effective policy, with defaults applied.
	Policy catalog.Policy
	// Facts are the lifecycle facts of each line, in any order.
	Facts []release.Facts
	// Availability maps each line to its installable builds. Nil means
	// the tool has no installability source.
	Availability map[string]Build
	// Now is the time the evaluation applies to.
	Now time.Time
}

// LineResult is the outcome for one candidate line.
type LineResult struct {
	// Line is the release line.
	Line string
	// Status is the line's status on the day of evaluation.
	Status release.Status
	// InMatrix reports that the line is in the matrix.
	InMatrix bool
	// Reason is why the line is excluded; "" when InMatrix.
	Reason string
	// LTS reports a long-term support line.
	LTS bool
	// Installable reports that Availability had a stable build.
	Installable bool
	// Newest is the newest patch release: Facts.Latest, else the
	// stable build, else the pre-release build.
	Newest string
	// EOL is the day the line reaches end-of-life, if known.
	EOL *time.Time
	// DaysToEOL is the whole days left until EOL, if known; negative
	// once it has passed.
	DaysToEOL *int
}

// Result is the outcome of Evaluate.
type Result struct {
	// Matrix holds the kept lines, ascending.
	Matrix []string
	// Latest is the newest line in Matrix that is not a pre-release,
	// else "".
	Latest string
	// Labels are Policy.Labels, for example "stable".
	Labels []string
	// Lines holds every candidate line, ascending.
	Lines []LineResult
	// Warnings are in a deterministic order: the installability
	// warning, then end-of-life warnings and advance warnings, each by
	// ascending line.
	Warnings []string
}

// candidate is the working state for one line during evaluation.
type candidate struct {
	LineResult
	facts    *release.Facts
	build    Build
	released *time.Time
}

func (c *candidate) eol() bool {
	return c.Status == release.StatusEOL || c.Status == release.StatusExtended
}

func (c *candidate) prerelease() bool {
	return c.Status == release.StatusPrerelease
}

func (c *candidate) exclude(reason string) {
	c.InMatrix = false
	c.Reason = reason
}

// Evaluate applies the policy in in to its facts and availability and
// returns the matrix and the outcome for every candidate line.
//
// The candidates are the lines in Facts and those that appear only in
// Availability. Each passes, in order, the pre-release, facts, bounds,
// admission and grace checks; the policy rule then chooses among the
// survivors, and the end-of-life behavior decides the end-of-life
// lines the policy names. Under the "fail" behavior Evaluate returns
// the full Result together with an error matching ErrEOL.
func Evaluate(in Input) (Result, error) {
	if err := validate(in); err != nil {
		return Result{}, err
	}
	cands := candidates(in)
	var res Result
	if in.Policy.Admit == catalog.AdmitInstallable && in.Availability == nil {
		res.Warnings = append(res.Warnings, WarnNoAvailability)
	}
	for _, c := range cands {
		screen(c, in)
	}
	applyRule(cands, in.Policy)
	warnings, failed := applyEOL(cands, in.Policy)
	res.Warnings = append(res.Warnings, warnings...)
	res.Warnings = append(res.Warnings, advanceWarnings(cands, in.Policy)...)

	if len(in.Policy.Labels) > 0 {
		res.Labels = slices.Clone(in.Policy.Labels)
	}
	for _, c := range cands {
		res.Lines = append(res.Lines, c.LineResult)
		if c.InMatrix {
			res.Matrix = append(res.Matrix, c.Line)
			if !c.prerelease() {
				res.Latest = c.Line
			}
		}
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("%w: %s", ErrEOL, strings.Join(failed, ", "))
	}
	return res, nil
}

func validate(in Input) error {
	if in.Scheme != release.SchemeMajor && in.Scheme != release.SchemeMajorMinor {
		return fmt.Errorf("unknown version scheme %q", in.Scheme)
	}
	if err := validatePolicy(in.Policy, in.Scheme); err != nil {
		return err
	}
	seen := make(map[string]bool, len(in.Facts))
	for _, f := range in.Facts {
		if !release.ValidLine(f.Line, in.Scheme) {
			return fmt.Errorf("facts line %q is not a %s line", f.Line, in.Scheme)
		}
		if seen[f.Line] {
			return fmt.Errorf("facts list line %q more than once", f.Line)
		}
		seen[f.Line] = true
	}
	for line := range in.Availability {
		if !release.ValidLine(line, in.Scheme) {
			return fmt.Errorf("availability line %q is not a %s line", line, in.Scheme)
		}
	}
	return nil
}

func validatePolicy(p catalog.Policy, scheme string) error {
	switch p.Rule {
	case catalog.RuleAll, catalog.RuleLTS:
	case catalog.RuleLatest:
		if p.Count < 1 {
			return fmt.Errorf("rule %q needs a count of at least 1, got %d", p.Rule, p.Count)
		}
	default:
		return fmt.Errorf("unknown policy rule %q", p.Rule)
	}
	switch p.Admit {
	case catalog.AdmitReleased, catalog.AdmitInstallable, catalog.AdmitExplicit:
	default:
		return fmt.Errorf("unknown admit value %q", p.Admit)
	}
	switch p.EOLBehaviour {
	case catalog.EOLAllow, catalog.EOLWarn, catalog.EOLStrip, catalog.EOLFail:
	default:
		return fmt.Errorf("unknown end-of-life behavior %q", p.EOLBehaviour)
	}
	if p.GraceDays < 0 || p.WarnDaysBeforeEOL < 0 {
		return errors.New("grace days and warning days must not be negative")
	}
	if p.Floor != "" && !release.ValidLine(p.Floor, scheme) {
		return fmt.Errorf("policy floor %q is not a %s line", p.Floor, scheme)
	}
	if p.Ceiling != "" && !release.ValidLine(p.Ceiling, scheme) {
		return fmt.Errorf("policy ceiling %q is not a %s line", p.Ceiling, scheme)
	}
	return nil
}

// candidates builds the candidate lines in ascending order, each with
// its status and descriptive fields filled in and provisionally in the
// matrix.
func candidates(in Input) []*candidate {
	byLine := make(map[string]*candidate)
	for i := range in.Facts {
		f := &in.Facts[i]
		byLine[f.Line] = &candidate{facts: f}
	}
	for line, b := range in.Availability {
		c, ok := byLine[line]
		if !ok {
			c = &candidate{}
			byLine[line] = c
		}
		c.build = b
	}
	out := make([]*candidate, 0, len(byLine))
	for line, c := range byLine {
		c.Line = line
		c.InMatrix = true
		c.Installable = c.build.Stable != ""
		describe(c, in.Now)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *candidate) int {
		return release.CompareLines(a.Line, b.Line)
	})
	return out
}

func describe(c *candidate, now time.Time) {
	c.Newest = c.build.Stable
	if c.Newest == "" {
		c.Newest = c.build.Prerelease
	}
	if c.facts == nil {
		c.Status = release.StatusUnknown
		if c.build.Prerelease != "" && c.build.Stable == "" {
			c.Status = release.StatusPrerelease
		}
		return
	}
	f := c.facts
	c.Status = release.StatusOf(*f, now)
	c.LTS = f.LTS != nil && *f.LTS
	c.released = f.Released
	c.EOL = f.EOLFrom
	if f.Latest != "" {
		c.Newest = f.Latest
	}
	if days, ok := release.DaysToEOL(*f, now); ok {
		c.DaysToEOL = &days
	}
}

// screen applies the pre-release, facts, bounds, admission and grace
// checks to one candidate.
func screen(c *candidate, in Input) {
	p := in.Policy
	switch {
	case c.prerelease():
		if !p.Prerelease || (in.Availability != nil && c.build.Prerelease == "") {
			c.exclude(ReasonPrerelease)
			return
		}
	case c.Status == release.StatusUnknown:
		c.exclude(ReasonNoFacts)
		return
	}
	if p.Floor != "" && release.CompareLines(c.Line, p.Floor) < 0 {
		c.exclude(ReasonBelowFloor)
		return
	}
	if p.Ceiling != "" && release.CompareLines(c.Line, p.Ceiling) > 0 {
		c.exclude(ReasonAboveCeiling)
		return
	}
	// A pre-release that reached here has its pre-release build, which
	// satisfies installable admission.
	if p.Admit == catalog.AdmitInstallable && in.Availability != nil && !c.prerelease() && !c.Installable {
		c.exclude(ReasonNotInstallable)
		return
	}
	if p.GraceDays > 0 && c.released != nil {
		until := utcDay(*c.released).AddDate(0, 0, p.GraceDays)
		if utcDay(in.Now).Before(until) {
			c.exclude(ReasonGrace)
		}
	}
}

// applyRule applies the policy rule to the candidates still in the
// matrix.
func applyRule(cands []*candidate, p catalog.Policy) {
	switch p.Rule {
	case catalog.RuleLTS:
		var current *candidate
		if p.Current {
			for _, c := range cands {
				if c.InMatrix && !c.LTS && !c.eol() {
					current = c
				}
			}
		}
		for _, c := range cands {
			if c.InMatrix && !c.LTS && c != current {
				c.exclude(ReasonNotLTS)
			}
		}
	case catalog.RuleLatest:
		kept := 0
		for i := len(cands) - 1; i >= 0; i-- {
			c := cands[i]
			if !c.InMatrix || c.prerelease() {
				continue
			}
			if kept < p.Count {
				kept++
			} else {
				c.exclude(ReasonNotLatest)
			}
		}
	}
}

// applyEOL decides the end-of-life lines still in the matrix. It
// returns the warnings it raises and, under the "fail" behavior, the
// lines that fail the evaluation.
func applyEOL(cands []*candidate, p catalog.Policy) (warnings, failed []string) {
	for _, c := range cands {
		if !c.InMatrix || !c.eol() {
			continue
		}
		// The "latest" rule has already cut the matrix to the newest
		// Count lines, which the policy names. "all" and "lts" name an
		// end-of-life line only through a floor, and the bounds have
		// already removed every line below it.
		if p.Rule != catalog.RuleLatest && p.Floor == "" {
			c.exclude(ReasonEOL)
			continue
		}
		switch p.EOLBehaviour {
		case catalog.EOLWarn:
			warnings = append(warnings, eolWarning(c, "kept in the matrix"))
		case catalog.EOLStrip:
			c.exclude(ReasonEOLStripped)
			warnings = append(warnings, eolWarning(c, "removed from the matrix"))
		case catalog.EOLFail:
			warnings = append(warnings, eolWarning(c, "kept in the matrix"))
			failed = append(failed, c.Line)
		}
	}
	return warnings, failed
}

func eolWarning(c *candidate, outcome string) string {
	when := "an unknown date"
	if c.EOL != nil {
		when = c.EOL.UTC().Format(time.DateOnly)
	}
	return fmt.Sprintf("line %s reached end-of-life on %s; %s", c.Line, when, outcome)
}

// advanceWarnings warns about each line in the matrix that is not yet
// end-of-life and reaches it within Policy.WarnDaysBeforeEOL days.
func advanceWarnings(cands []*candidate, p catalog.Policy) []string {
	if p.WarnDaysBeforeEOL == 0 {
		return nil
	}
	var out []string
	for _, c := range cands {
		if !c.InMatrix || c.eol() || c.DaysToEOL == nil || c.EOL == nil {
			continue
		}
		if d := *c.DaysToEOL; d >= 0 && d <= p.WarnDaysBeforeEOL {
			out = append(out, fmt.Sprintf("line %s reaches end-of-life on %s, in %d days",
				c.Line, c.EOL.UTC().Format(time.DateOnly), d))
		}
	}
	return out
}

// utcDay returns midnight UTC at the start of t's UTC calendar day.
func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// CheckResult answers whether one version is supported.
type CheckResult struct {
	// Line is the version's release line.
	Line string
	// Status is the line's status; StatusUnknown when the Result does
	// not list the line.
	Status release.Status
	// InMatrix reports that the line is in the matrix.
	InMatrix bool
	// Supported reports a line upstream supports, active or security,
	// that is in the matrix.
	Supported bool
	// PolicyExcluded reports a line upstream supports that the policy
	// keeps out of the matrix.
	PolicyExcluded bool
	// Reason is why the line is excluded, as in LineResult.
	Reason string
	// EOL is the day the line reaches end-of-life, if known.
	EOL *time.Time
	// DaysToEOL is the whole days left until EOL, if known.
	DaysToEOL *int
}

// Check parses version under scheme and reports how res treats its
// release line. A version that does not parse returns the error from
// release.Parse, which matches release.ErrNotAVersion for a label such
// as "stable".
func Check(res Result, scheme, version string) (CheckResult, error) {
	v, err := release.Parse(scheme, version)
	if err != nil {
		return CheckResult{}, err
	}
	out := CheckResult{Line: v.Line, Status: release.StatusUnknown}
	for _, l := range res.Lines {
		if release.CompareLines(l.Line, v.Line) != 0 {
			continue
		}
		upstream := l.Status == release.StatusActive || l.Status == release.StatusSecurity
		out.Status = l.Status
		out.InMatrix = l.InMatrix
		out.Supported = upstream && l.InMatrix
		out.PolicyExcluded = upstream && !l.InMatrix
		out.Reason = l.Reason
		out.EOL = l.EOL
		out.DaysToEOL = l.DaysToEOL
		break
	}
	return out, nil
}
