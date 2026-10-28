// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/detect"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// MatrixRequest describes the project a matrix is for.
type MatrixRequest struct {
	// Constraint is explicit constraint text, in the tool's constraint
	// syntax. It wins over detection.
	Constraint string
	// ProjectDir is where to detect a constraint when Constraint is
	// empty; "" detects nothing.
	ProjectDir string
	// Strict fails, rather than widens, a constraint that allows no
	// line of the policy matrix.
	Strict bool
}

// projectConstraint is the constraint a request names.
type projectConstraint struct {
	c        *constraint.Constraint
	text     string
	origin   string
	warnings []string
}

// Matrix resolves the matrix of tool, an id or alias, for the project
// req describes.
//
// Under the "fail" end-of-life behavior a matrix naming an end-of-life
// line returns the full report together with an error wrapping
// policy.ErrEOL. Under req.Strict, a constraint that allows no line of
// the policy matrix returns the report together with an error matching
// ErrConstraintOutOfRange. Any other error returns a zero Report.
func (r *Resolver) Matrix(ctx context.Context, tool string, req MatrixRequest) (Report, error) {
	id, t, err := r.lookup(tool)
	if err != nil {
		return Report{}, err
	}
	pc, err := r.constraintFor(t, req)
	if err != nil {
		return Report{}, err
	}
	f, err := r.gather(ctx, t, "")
	if err != nil {
		return Report{}, err
	}
	now := r.now()
	res, evalErr := r.evaluate(t, f, now)
	if evalErr != nil && !errors.Is(evalErr, policy.ErrEOL) {
		return Report{}, evalErr
	}

	rep := r.report(id, t, f, res, now)
	rep.Warnings = append(rep.Warnings, pc.warnings...)
	lines, outcome := constraint.Apply(res.Matrix, pc.c)
	var strictErr error
	if outcome != constraint.OutcomeNone {
		rep.Constraint = &ConstraintSummary{Text: pc.text, Origin: pc.origin, Outcome: outcome}
	}
	if outcome == constraint.OutcomeOutOfRangeFallback {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"constraint %q allows no line of the matrix; the matrix is unconstrained", pc.text))
		if req.Strict {
			strictErr = fmt.Errorf("%w: %q", ErrConstraintOutOfRange, pc.text)
		}
	}
	setMatrix(&rep, res, lines)
	return rep, errors.Join(evalErr, strictErr)
}

// constraintFor parses the explicit constraint of req, or detects one
// in req.ProjectDir.
func (r *Resolver) constraintFor(t catalog.Tool, req MatrixRequest) (projectConstraint, error) {
	switch {
	case req.Constraint != "":
		if t.Constraints == nil || t.Constraints.Syntax == "" {
			return projectConstraint{}, fmt.Errorf("tool %q declares no constraint syntax", t.Name)
		}
		c, err := constraint.Parse(t.Constraints.Syntax, t.VersionScheme, req.Constraint)
		if err != nil {
			return projectConstraint{}, fmt.Errorf("constraint %q: %w", req.Constraint, err)
		}
		return projectConstraint{c: &c, text: req.Constraint}, nil
	case req.ProjectDir != "":
		d, err := detect.Detect(req.ProjectDir, t)
		if err != nil {
			return projectConstraint{}, err
		}
		return projectConstraint{c: d.Constraint, text: d.Text, origin: d.Origin, warnings: d.Warnings}, nil
	default:
		return projectConstraint{}, nil
	}
}

// Check reports whether version is supported under the policy of tool,
// an id or alias. A version that is not a version at all, such as
// "stable", returns an error wrapping release.ErrNotAVersion.
//
// When the policy can be decided from one line, the "all" rule or the
// "lts" rule without "current", Check fetches only the version's line;
// the report then describes that line alone. A line the upstream does
// not know has status "unknown". No project constraint applies, and an
// end-of-life failure of the policy is reported as a warning only.
func (r *Resolver) Check(ctx context.Context, tool, version string) (Report, error) {
	id, t, err := r.lookup(tool)
	if err != nil {
		return Report{}, err
	}
	v, err := release.Parse(t.VersionScheme, version)
	if err != nil {
		return Report{}, err
	}
	line := ""
	p := t.Policy
	if p.Rule == catalog.RuleAll || (p.Rule == catalog.RuleLTS && !p.Current) {
		line = v.Line
	}
	f, err := r.gather(ctx, t, line)
	if err != nil {
		return Report{}, err
	}
	now := r.now()
	res, err := r.evaluate(t, f, now)
	if err != nil && !errors.Is(err, policy.ErrEOL) {
		return Report{}, err
	}
	cr, err := policy.Check(res, t.VersionScheme, version)
	if err != nil {
		return Report{}, err
	}
	rep := r.report(id, t, f, res, now)
	setMatrix(&rep, res, res.Matrix)
	rep.Check = &CheckSummary{
		Version:        version,
		Line:           cr.Line,
		Status:         cr.Status,
		Supported:      cr.Supported,
		PolicyExcluded: cr.PolicyExcluded,
		InMatrix:       cr.InMatrix,
		Reason:         cr.Reason,
		EOL:            formatDate(cr.EOL),
		DaysToEOL:      cr.DaysToEOL,
	}
	return rep, nil
}

func (r *Resolver) evaluate(t catalog.Tool, f facts, now time.Time) (policy.Result, error) {
	return policy.Evaluate(policy.Input{
		Scheme:       t.VersionScheme,
		Policy:       t.Policy,
		Facts:        f.facts,
		Availability: f.availability,
		Now:          now,
	})
}

// report builds everything but the matrix, latest and the constraint.
func (r *Resolver) report(id string, t catalog.Tool, f facts, res policy.Result, now time.Time) Report {
	rep := Report{
		Schema:      ReportSchema,
		Tool:        id,
		Name:        t.Name,
		GeneratedAt: stamp(now),
		Source:      SourceSummary{Kind: KindLive, Sources: f.sources},
		Policy:      r.policySummary(id, t.Policy),
		Matrix:      Matrix{Key: t.MatrixKey, Versions: []string{}},
		Lines:       []Line{},
		Warnings:    []string{},
	}
	if rep.Source.Sources == nil {
		rep.Source.Sources = []SourceEntry{}
	}
	for _, s := range f.sources {
		if s.Kind.rank() > rep.Source.Kind.rank() {
			rep.Source.Kind = s.Kind
		}
	}
	rep.Warnings = append(rep.Warnings, f.warnings...)
	if f.snapshot {
		rep.Source.SnapshotDate = r.snapshot.GeneratedAt.UTC().Format(time.DateOnly)
		if w := r.snapshot.Warning(now); w != "" {
			rep.Warnings = append(rep.Warnings, w)
		}
	}
	rep.Warnings = append(rep.Warnings, res.Warnings...)

	byLine := make(map[string]release.Facts, len(f.facts))
	for _, fa := range f.facts {
		byLine[fa.Line] = fa
	}
	for _, l := range res.Lines {
		fa := byLine[l.Line]
		rep.Lines = append(rep.Lines, Line{
			Line:        l.Line,
			Status:      l.Status,
			InMatrix:    l.InMatrix,
			Reason:      l.Reason,
			LTS:         l.LTS,
			Installable: l.Installable,
			Newest:      l.Newest,
			Released:    formatDate(fa.Released),
			EOAS:        formatDate(fa.EOASFrom),
			EOL:         formatDate(l.EOL),
			DaysToEOL:   l.DaysToEOL,
		})
	}
	return rep
}

func (r *Resolver) policySummary(id string, p catalog.Policy) PolicySummary {
	s := PolicySummary{
		Rule:              p.Rule,
		Count:             p.Count,
		Floor:             p.Floor,
		Ceiling:           p.Ceiling,
		Admit:             p.Admit,
		Prerelease:        p.Prerelease,
		GraceDays:         p.GraceDays,
		EOLBehaviour:      p.EOLBehaviour,
		WarnDaysBeforeEOL: p.WarnDaysBeforeEOL,
		Labels:            slices.Clone(p.Labels),
	}
	if p.Rule == catalog.RuleLTS {
		current := p.Current
		s.Current = &current
	}
	if r.overlaid(id) {
		s.Overlay = true
		s.Origin = r.overlayOrigin
	}
	return s
}

// setMatrix sets the final matrix to lines, a subset of the policy
// matrix, and marks the lines the constraint removed.
func setMatrix(rep *Report, res policy.Result, lines []string) {
	rep.Matrix.Versions = append(slices.Clone(lines), res.Labels...)
	if rep.Matrix.Versions == nil {
		rep.Matrix.Versions = []string{}
	}
	for i := range rep.Lines {
		l := &rep.Lines[i]
		if !l.InMatrix {
			continue
		}
		if !slices.Contains(lines, l.Line) {
			l.InMatrix = false
			l.Reason = ReasonConstraint
			continue
		}
		if l.Status != release.StatusPrerelease {
			rep.Latest = l.Line
		}
	}
}

// rank orders kinds from the best to the worst.
func (k Kind) rank() int {
	switch k {
	case KindLive:
		return 0
	case KindCache:
		return 1
	case KindStale:
		return 2
	case KindSnapshot:
		return 3
	default:
		return 4
	}
}

// formatDate formats a known day as YYYY-MM-DD, and an unknown one as
// "", which the report omits.
func formatDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.DateOnly)
}

// stamp returns t in UTC, truncated to the second.
func stamp(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}
