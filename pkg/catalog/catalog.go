// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package catalog loads, validates and merges the tool catalog.
//
// The catalog defines every tool once: its identity, where its release
// facts come from, how projects declare constraints on it and the
// policy that selects its supported release lines. Tool versions live
// in that data, never in Go source.
//
// Parsing runs YAML through a JSON-compatible value, the JSON Schema
// in catalog/schema, a strict typed decode, policy defaults and then
// the semantic checks a schema cannot express. Every problem found is
// reported, each as a *FieldError naming its path, joined with
// errors.Join.
package catalog

import (
	"fmt"
	"os"
	"sort"
	"strings"

	catalogdata "github.com/lfreleng-actions/supported-versions-action/catalog"
)

// Catalog is a parsed and validated tool catalog.
type Catalog struct {
	// Schema is the catalog format version.
	Schema int `json:"schema"`
	// Tools maps each tool id to its definition.
	Tools map[string]Tool `json:"tools"`
}

// Tool defines one tool.
type Tool struct {
	// Name is the display name.
	Name string `json:"name"`
	// Aliases are alternative lower-case names accepted by Lookup.
	Aliases []string `json:"aliases,omitempty"`
	// VersionScheme is SchemeMajor or SchemeMajorMinor and defines what
	// a release line looks like for this tool.
	VersionScheme string `json:"version_scheme"`
	// MatrixKey is the key a matrix job uses for this tool's version.
	MatrixKey string `json:"matrix_key"`
	// Sources lists where release facts for this tool come from.
	Sources []Source `json:"sources"`
	// Constraints describes how projects declare a version constraint.
	Constraints *Constraints `json:"constraints,omitempty"`
	// Policy selects the supported release lines.
	Policy Policy `json:"policy"`
	// Compatibility maps another tool id to a map from this tool's
	// release line to the range of the other tool's lines it supports.
	Compatibility map[string]map[string]LineRange `json:"compatibility,omitempty"`
}

// Source names one origin of release facts. Type selects which of the
// other fields apply.
type Source struct {
	// Type is the source type, for example "endoflife".
	Type string `json:"type"`
	// Product is the endoflife product name.
	Product string `json:"product,omitempty"`
	// Repository is an owner/name GitHub repository.
	Repository string `json:"repository,omitempty"`
	// Artifact is a Maven group:artifact coordinate.
	Artifact string `json:"artifact,omitempty"`
	// Lines are the release lines of a static source.
	Lines []StaticLine `json:"lines,omitempty"`
}

// StaticLine is one release line declared inline by a static source.
type StaticLine struct {
	// Line is the release line.
	Line string `json:"line"`
	// Released is the release date, YYYY-MM-DD.
	Released string `json:"released,omitempty"`
	// EOL is the end-of-life date, YYYY-MM-DD.
	EOL string `json:"eol,omitempty"`
	// LTS marks a long-term support line.
	LTS bool `json:"lts,omitempty"`
}

// Constraints describes how projects declare a version constraint.
type Constraints struct {
	// Syntax is the constraint grammar, for example "pep440".
	Syntax string `json:"syntax,omitempty"`
	// Detect lists the places a constraint may be found.
	Detect []DetectRule `json:"detect,omitempty"`
}

// DetectRule names a file and at most one locator within it.
type DetectRule struct {
	// File is the file to inspect.
	File string `json:"file"`
	// Field is a dotted path into a structured file.
	Field string `json:"field,omitempty"`
	// Classifiers is a trove classifier template.
	Classifiers string `json:"classifiers,omitempty"`
	// Regex is a pattern whose first group captures the constraint.
	Regex string `json:"regex,omitempty"`
	// Directive is a directive name, for example in go.mod.
	Directive string `json:"directive,omitempty"`
	// XPath locates the constraint in an XML file.
	XPath string `json:"xpath,omitempty"`
	// Key names a key in a flat key/value file.
	Key string `json:"key,omitempty"`
}

// LineRange bounds the release lines of another tool.
type LineRange struct {
	// Min is the oldest supported line, if bounded.
	Min string `json:"min,omitempty"`
	// Max is the newest supported line, if bounded.
	Max string `json:"max,omitempty"`
}

// Policy selects a tool's supported release lines. Parsing fills in
// the documented defaults for fields the catalog omits.
type Policy struct {
	// Rule is RuleAll, RuleLTS or RuleLatest.
	Rule string `json:"rule"`
	// Count is the number of lines RuleLatest keeps; zero otherwise.
	Count int `json:"count,omitempty"`
	// Current adds the newest non-LTS line under RuleLTS.
	Current bool `json:"current"`
	// Floor is the oldest line to consider, if set.
	Floor string `json:"floor,omitempty"`
	// Ceiling is the newest line to consider, if set.
	Ceiling string `json:"ceiling,omitempty"`
	// Admit decides when a new line becomes eligible.
	Admit string `json:"admit"`
	// Prerelease admits pre-release lines.
	Prerelease bool `json:"prerelease"`
	// GraceDays keeps a line for this many days after its EOL.
	GraceDays int `json:"grace_days"`
	// EOLBehaviour decides what happens to an end-of-life line.
	EOLBehaviour string `json:"eol_behaviour"` //nolint:misspell // field name fixed by the catalog format
	// WarnDaysBeforeEOL starts warnings this many days before EOL.
	WarnDaysBeforeEOL int `json:"warn_days_before_eol"`
	// Labels are extra channel labels, for example "stable".
	Labels []string `json:"labels,omitempty"`
}

// Version schemes.
const (
	// SchemeMajor lines are a single number.
	SchemeMajor = "major"
	// SchemeMajorMinor lines are two dot-separated numbers.
	SchemeMajorMinor = "major.minor"
)

// Policy rules.
const (
	// RuleAll keeps every supported line.
	RuleAll = "all"
	// RuleLTS keeps long-term support lines.
	RuleLTS = "lts"
	// RuleLatest keeps the newest Count lines.
	RuleLatest = "latest"
)

// Admit values.
const (
	// AdmitReleased admits a line once it is released.
	AdmitReleased = "released"
	// AdmitInstallable admits a line once it can be installed.
	AdmitInstallable = "installable"
	// AdmitExplicit admits lines only up to an explicit Ceiling.
	AdmitExplicit = "explicit"
)

// Policy defaults applied to fields a catalog omits.
const (
	// DefaultAdmit is the default Policy.Admit.
	DefaultAdmit = AdmitInstallable
	// DefaultEOLBehaviour is the default Policy.EOLBehaviour.
	DefaultEOLBehaviour = "warn"
	// DefaultWarnDaysBeforeEOL is the default Policy.WarnDaysBeforeEOL.
	DefaultWarnDaysBeforeEOL = 90
)

// Embedded parses and validates the catalog embedded in the binary.
func Embedded() (*Catalog, error) {
	return Parse(catalogdata.Catalog)
}

// Parse parses and validates a catalog from YAML bytes.
func Parse(data []byte) (*Catalog, error) {
	var raw rawCatalog
	if err := decode(data, catalogSchema, &raw); err != nil {
		return nil, err
	}
	c := &Catalog{Schema: raw.Schema, Tools: make(map[string]Tool, len(raw.Tools))}
	for id, rt := range raw.Tools {
		c.Tools[id] = Tool{
			Name:          rt.Name,
			Aliases:       rt.Aliases,
			VersionScheme: rt.VersionScheme,
			MatrixKey:     rt.MatrixKey,
			Sources:       rt.Sources,
			Constraints:   rt.Constraints,
			Policy:        rt.Policy.withDefaults(),
			Compatibility: rt.Compatibility,
		}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// Load reads, parses and validates the catalog at path.
func Load(path string) (*Catalog, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the caller chooses the catalog to load
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	return Parse(data)
}

// Lookup resolves a tool id or alias, ignoring case. It returns the
// tool id, the tool and whether the name was found.
func (c *Catalog) Lookup(name string) (id string, tool Tool, ok bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	if t, found := c.Tools[want]; found {
		return want, t, true
	}
	for tid, t := range c.Tools {
		for _, a := range t.Aliases {
			if a == want {
				return tid, t, true
			}
		}
	}
	return "", Tool{}, false
}

// IDs returns the tool ids in sorted order.
func (c *Catalog) IDs() []string {
	ids := make([]string, 0, len(c.Tools))
	for id := range c.Tools {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// rawCatalog mirrors Catalog before policy defaults are applied.
type rawCatalog struct {
	Schema int                `json:"schema"`
	Tools  map[string]rawTool `json:"tools"`
}

type rawTool struct {
	Name          string                          `json:"name"`
	Aliases       []string                        `json:"aliases"`
	VersionScheme string                          `json:"version_scheme"`
	MatrixKey     string                          `json:"matrix_key"`
	Sources       []Source                        `json:"sources"`
	Constraints   *Constraints                    `json:"constraints"`
	Policy        PolicyOverride                  `json:"policy"`
	Compatibility map[string]map[string]LineRange `json:"compatibility"`
}
