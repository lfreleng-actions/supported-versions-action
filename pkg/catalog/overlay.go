// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package catalog

import (
	"errors"
	"fmt"
	"os"
	"slices"
)

// Overlay adjusts the policy of tools in a catalog without changing
// the catalog itself. Only policy fields may be overridden.
type Overlay struct {
	// Schema is the overlay format version.
	Schema int `json:"schema"`
	// Tools maps a catalog tool id (not an alias) to its adjustments.
	Tools map[string]ToolOverlay `json:"tools"`
}

// ToolOverlay holds the adjustments for one tool.
type ToolOverlay struct {
	// Policy holds the policy fields to replace.
	Policy PolicyOverride `json:"policy"`
}

// PolicyOverride holds policy fields that may each be unset. A nil
// field leaves the catalog's value alone; a non-nil field replaces it,
// even when it points at a zero value.
type PolicyOverride struct {
	// Rule replaces Policy.Rule.
	Rule *string `json:"rule,omitempty"`
	// Count replaces Policy.Count.
	Count *int `json:"count,omitempty"`
	// Current replaces Policy.Current.
	Current *bool `json:"current,omitempty"`
	// Floor replaces Policy.Floor.
	Floor *string `json:"floor,omitempty"`
	// Ceiling replaces Policy.Ceiling.
	Ceiling *string `json:"ceiling,omitempty"`
	// Admit replaces Policy.Admit.
	Admit *string `json:"admit,omitempty"`
	// Prerelease replaces Policy.Prerelease.
	Prerelease *bool `json:"prerelease,omitempty"`
	// GraceDays replaces Policy.GraceDays.
	GraceDays *int `json:"grace_days,omitempty"`
	// EOLBehaviour replaces Policy.EOLBehaviour.
	EOLBehaviour *string `json:"eol_behaviour,omitempty"` //nolint:misspell // field name fixed by the catalog format
	// WarnDaysBeforeEOL replaces Policy.WarnDaysBeforeEOL.
	WarnDaysBeforeEOL *int `json:"warn_days_before_eol,omitempty"`
	// Labels replaces Policy.Labels.
	Labels []string `json:"labels,omitempty"`
}

// ParseOverlay parses and validates a policy overlay from YAML bytes.
// Whether its tool ids exist is checked by Catalog.Apply.
func ParseOverlay(data []byte) (*Overlay, error) {
	var o Overlay
	if err := decode(data, overlaySchema, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// LoadOverlay reads, parses and validates the overlay at path.
func LoadOverlay(path string) (*Overlay, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the caller chooses the overlay to load
	if err != nil {
		return nil, fmt.Errorf("read overlay: %w", err)
	}
	return ParseOverlay(data)
}

// Apply returns a new catalog with the overlay's policy fields merged
// in, and validates the result. The overlay is checked against the
// overlay schema first, so one built in code rather than parsed meets
// the same rules, and the merged catalog against the catalog schema
// and the semantic checks. The receiver is never modified. A nil
// overlay yields an unchanged copy.
func (c *Catalog) Apply(o *Overlay) (*Catalog, error) {
	out := c.clone()
	if o == nil {
		return out, nil
	}
	if err := checkSchema(overlaySchema, o); err != nil {
		return nil, err
	}
	var errs []error
	for _, id := range sortedKeys(o.Tools) {
		tool, ok := out.Tools[id]
		if !ok {
			path := "tools." + id
			if aliasOf, _, found := c.Lookup(id); found {
				errs = append(errs, fieldErrorf(path, "%q is an alias of tool %q; overlays must use the tool id", id, aliasOf))
			} else {
				errs = append(errs, fieldErrorf(path, "unknown tool %q", id))
			}
			continue
		}
		tool.Policy = o.Tools[id].Policy.applyTo(tool.Policy)
		out.Tools[id] = tool
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if err := checkSchema(catalogSchema, out); err != nil {
		return nil, err
	}
	if err := out.validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// withDefaults returns the policy with documented defaults filled in
// for unset fields.
func (p PolicyOverride) withDefaults() Policy {
	return p.applyTo(Policy{
		Admit:             DefaultAdmit,
		EOLBehaviour:      DefaultEOLBehaviour,
		WarnDaysBeforeEOL: DefaultWarnDaysBeforeEOL,
	})
}

// applyTo returns base with every set field of p replacing its value.
func (p PolicyOverride) applyTo(base Policy) Policy {
	setIf(&base.Rule, p.Rule)
	setIf(&base.Count, p.Count)
	setIf(&base.Current, p.Current)
	setIf(&base.Floor, p.Floor)
	setIf(&base.Ceiling, p.Ceiling)
	setIf(&base.Admit, p.Admit)
	setIf(&base.Prerelease, p.Prerelease)
	setIf(&base.GraceDays, p.GraceDays)
	setIf(&base.EOLBehaviour, p.EOLBehaviour)
	setIf(&base.WarnDaysBeforeEOL, p.WarnDaysBeforeEOL)
	if p.Labels != nil {
		base.Labels = slices.Clone(p.Labels)
	}
	return base
}

func setIf[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

// clone returns a deep copy of the catalog.
func (c *Catalog) clone() *Catalog {
	out := &Catalog{Schema: c.Schema, Tools: make(map[string]Tool, len(c.Tools))}
	for id, t := range c.Tools {
		out.Tools[id] = t.clone()
	}
	return out
}

func (t Tool) clone() Tool {
	t.Aliases = slices.Clone(t.Aliases)
	t.Sources = slices.Clone(t.Sources)
	for i := range t.Sources {
		t.Sources[i].Lines = slices.Clone(t.Sources[i].Lines)
	}
	if t.Constraints != nil {
		cons := *t.Constraints
		cons.Detect = slices.Clone(cons.Detect)
		t.Constraints = &cons
	}
	t.Policy.Labels = slices.Clone(t.Policy.Labels)
	if t.Compatibility != nil {
		compat := make(map[string]map[string]LineRange, len(t.Compatibility))
		for other, ranges := range t.Compatibility {
			m := make(map[string]LineRange, len(ranges))
			for line, r := range ranges {
				m[line] = r
			}
			compat[other] = m
		}
		t.Compatibility = compat
	}
	return t
}
