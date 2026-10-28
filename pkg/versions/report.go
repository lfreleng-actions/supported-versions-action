// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// ReportSchema is the report format version, the report's "schema".
const ReportSchema = 1

// ReasonConstraint is the reason of a line the policy kept that the
// project constraint removed from the matrix. The other reasons are
// those of package policy.
const ReasonConstraint = "constraint"

// Kind says where release facts came from. Kinds are ordered from the
// best to the worst: live, cache, stale, snapshot, unavailable.
type Kind string

// Kinds of release facts.
const (
	// KindLive is a response fetched or revalidated just now.
	KindLive Kind = "live"
	// KindCache is a cached response younger than the maximum age.
	KindCache Kind = "cache"
	// KindStale is a cached response older than the maximum age.
	KindStale Kind = "stale"
	// KindSnapshot is the offline snapshot embedded in the binary.
	KindSnapshot Kind = "snapshot"
	// KindUnavailable is a source that neither the network, the cache
	// nor the snapshot could supply. Only an installability source
	// may be unavailable; the policy then admits released lines.
	KindUnavailable Kind = "unavailable"
)

// Report is the outcome of Resolver.Matrix or Resolver.Check. Its JSON
// form is described by catalog/schema/report.schema.json. Times are
// RFC 3339 in UTC and dates are YYYY-MM-DD.
type Report struct {
	// Schema is the report format version, always ReportSchema.
	Schema int `json:"schema"`
	// Tool is the catalog tool id.
	Tool string `json:"tool"`
	// Name is the tool's display name.
	Name string `json:"name"`
	// GeneratedAt is when the report was made, in UTC, to the second.
	GeneratedAt time.Time `json:"generated_at"`
	// Source says where the release facts came from.
	Source SourceSummary `json:"source"`
	// Policy is the effective policy.
	Policy PolicySummary `json:"policy"`
	// Constraint is the project constraint; nil when none applied.
	Constraint *ConstraintSummary `json:"constraint,omitempty"`
	// Matrix is the final matrix.
	Matrix Matrix `json:"matrix"`
	// Latest is the newest line in the matrix that is not a
	// pre-release, else "". A label never counts.
	Latest string `json:"latest"`
	// Lines holds every candidate line, ascending.
	Lines []Line `json:"lines"`
	// Warnings are in a deterministic order: source warnings in
	// catalog order, the snapshot age warning, the policy warnings,
	// then the constraint warnings.
	Warnings []string `json:"warnings"`
	// Check answers Resolver.Check; nil from Resolver.Matrix.
	Check *CheckSummary `json:"check,omitempty"`
}

// SourceSummary says where the release facts came from.
type SourceSummary struct {
	// Kind is the worst Kind over Sources.
	Kind Kind `json:"kind"`
	// SnapshotDate is the day the snapshot was generated, set when any
	// source used it.
	SnapshotDate string `json:"snapshot_date,omitempty"`
	// Sources holds one entry per catalog source, in catalog order.
	Sources []SourceEntry `json:"sources"`
}

// SourceEntry is where one catalog source's facts came from.
type SourceEntry struct {
	// Type is the catalog source type, for example "endoflife".
	Type string `json:"type"`
	// Ref names the upstream: an endoflife product or a repository.
	Ref string `json:"ref"`
	// Kind is where the facts came from.
	Kind Kind `json:"kind"`
	// FetchedAt is when the facts were fetched; nil when unavailable.
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
}

// PolicySummary echoes the effective policy, after any overlay.
type PolicySummary struct {
	// Rule is the policy rule.
	Rule string `json:"rule"`
	// Count is the number of lines the "latest" rule keeps, if set.
	Count int `json:"count,omitempty"`
	// Current is set only under the "lts" rule.
	Current *bool `json:"current,omitempty"`
	// Floor is the oldest line considered, if set.
	Floor string `json:"floor,omitempty"`
	// Ceiling is the newest line considered, if set.
	Ceiling string `json:"ceiling,omitempty"`
	// Admit decides when a new line becomes eligible.
	Admit string `json:"admit"`
	// Prerelease admits pre-release lines.
	Prerelease bool `json:"prerelease"`
	// GraceDays delays a newly released line by this many days.
	GraceDays int `json:"grace_days"`
	// EOLBehaviour decides what happens to an end-of-life line.
	EOLBehaviour string `json:"eol_behaviour"` //nolint:misspell // field name fixed by the catalog format
	// WarnDaysBeforeEOL starts warnings this many days before EOL.
	WarnDaysBeforeEOL int `json:"warn_days_before_eol"`
	// Labels are the channel labels appended to the matrix.
	Labels []string `json:"labels,omitempty"`
	// Overlay reports that an overlay changed this tool's policy.
	Overlay bool `json:"overlay"`
	// Origin names the overlay, as Options.OverlayOrigin; "" when
	// Overlay is false.
	Origin string `json:"origin"`
}

// ConstraintSummary describes the project constraint applied.
type ConstraintSummary struct {
	// Text is the constraint text as given or found.
	Text string `json:"text"`
	// Origin names where a detected constraint was found, as the file
	// and locator; "" for an explicit constraint.
	Origin string `json:"origin,omitempty"`
	// Outcome is what applying the constraint did to the matrix.
	Outcome constraint.Outcome `json:"outcome"`
}

// Matrix is the final matrix.
type Matrix struct {
	// Key is the key a matrix job uses for the tool's version.
	Key string `json:"key"`
	// Versions are the lines, ascending, then the policy labels.
	Versions []string `json:"versions"`
}

// Line is the outcome for one candidate release line.
type Line struct {
	// Line is the release line.
	Line string `json:"line"`
	// Status is the line's status on the day of the report.
	Status release.Status `json:"status"`
	// InMatrix reports that the line is in the final matrix.
	InMatrix bool `json:"in_matrix"`
	// Reason is why the line is excluded; "" when InMatrix.
	Reason string `json:"reason,omitempty"`
	// LTS reports a long-term support line.
	LTS bool `json:"lts"`
	// Installable reports a stable build the setup action installs.
	Installable bool `json:"installable"`
	// Newest is the newest patch release, if known.
	Newest string `json:"newest,omitempty"`
	// Released is the release date, if known.
	Released string `json:"released,omitempty"`
	// EOAS is the day active support ends, if known.
	EOAS string `json:"eoas,omitempty"`
	// EOL is the day the line reaches end-of-life, if known.
	EOL string `json:"eol,omitempty"`
	// DaysToEOL is the whole days left until EOL, if known.
	DaysToEOL *int `json:"days_to_eol,omitempty"`
}

// CheckSummary answers whether one version is supported.
type CheckSummary struct {
	// Version is the version as given.
	Version string `json:"version"`
	// Line is the version's release line.
	Line string `json:"line"`
	// Status is the line's status; "unknown" when no source lists it.
	Status release.Status `json:"status"`
	// Supported reports a line upstream supports that is in the
	// matrix.
	Supported bool `json:"supported"`
	// PolicyExcluded reports a line upstream supports that the policy
	// keeps out of the matrix.
	PolicyExcluded bool `json:"policy_excluded"`
	// InMatrix reports that the line is in the matrix.
	InMatrix bool `json:"in_matrix"`
	// Reason is why the line is excluded, as in Line.
	Reason string `json:"reason,omitempty"`
	// EOL is the day the line reaches end-of-life, if known.
	EOL string `json:"eol,omitempty"`
	// DaysToEOL is the whole days left until EOL, if known.
	DaysToEOL *int `json:"days_to_eol,omitempty"`
}

// JSON encodes the report with two-space indentation and a trailing
// newline.
func (r Report) JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, fmt.Errorf("versions: encode report: %w", err)
	}
	return buf.Bytes(), nil
}
