// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package catalog

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// validate applies the checks the JSON Schema cannot express and
// returns every problem found, joined.
func (c *Catalog) validate() error {
	return joinFieldErrors(c.check(nil))
}

// check applies the checks the JSON Schema cannot express and returns
// every problem found. excluded names tools left out of c because they
// failed the schema: their ids still count as taken, and compatibility
// with them is not checked, since their definitions cannot be trusted.
func (c *Catalog) check(excluded map[string]bool) []*FieldError {
	var errs []*FieldError
	add := func(fe *FieldError) { errs = append(errs, fe) }

	owners := make(map[string]string, len(c.Tools)+len(excluded))
	for id := range c.Tools {
		owners[id] = "tool id " + strconv.Quote(id)
	}
	for id := range excluded {
		owners[id] = "tool id " + strconv.Quote(id)
	}
	ids := c.IDs()
	for _, id := range ids {
		for i, a := range c.Tools[id].Aliases {
			path := fmt.Sprintf("tools.%s.aliases[%d]", id, i)
			if prev, taken := owners[a]; taken {
				add(fieldErrorf(path, "%q duplicates %s", a, prev))
				continue
			}
			owners[a] = fmt.Sprintf("an alias of tool %q", id)
		}
	}

	for _, id := range ids {
		t := c.Tools[id]
		base := "tools." + id
		scheme := t.VersionScheme
		checkLine := func(path, line, sch string) bool {
			if !release.ValidLine(line, sch) {
				add(fieldErrorf(path, "%q is not a %s line", line, sch))
				return false
			}
			return true
		}

		for si, s := range t.Sources {
			for li, l := range s.Lines {
				lp := fmt.Sprintf("%s.sources[%d].lines[%d]", base, si, li)
				checkLine(lp+".line", l.Line, scheme)
				for _, d := range [...]struct{ name, value string }{{"released", l.Released}, {"eol", l.EOL}} {
					if d.value == "" {
						continue
					}
					if _, err := time.Parse(time.DateOnly, d.value); err != nil {
						add(fieldErrorf(lp+"."+d.name, "%q is not a valid YYYY-MM-DD date", d.value))
					}
				}
			}
		}

		validatePolicy(t.Policy, base+".policy", scheme, checkLine, add)

		for _, other := range sortedKeys(t.Compatibility) {
			cp := base + ".compatibility." + other
			otherTool, found := c.Tools[other]
			switch {
			case other == id:
				add(fieldErrorf(cp, "a tool cannot declare compatibility with itself"))
				continue
			case excluded[other]:
				continue
			case !found:
				add(fieldErrorf(cp, "unknown tool %q", other))
				continue
			}
			ranges := t.Compatibility[other]
			for _, line := range sortedKeys(ranges) {
				lp := cp + "." + line
				checkLine(lp, line, scheme)
				r := ranges[line]
				minOK := r.Min == "" || checkLine(lp+".min", r.Min, otherTool.VersionScheme)
				maxOK := r.Max == "" || checkLine(lp+".max", r.Max, otherTool.VersionScheme)
				if r.Min != "" && r.Max != "" && minOK && maxOK && release.CompareLines(r.Min, r.Max) > 0 {
					add(fieldErrorf(lp+".min", "%q is above max %q", r.Min, r.Max))
				}
			}
		}
	}
	return errs
}

func validatePolicy(p Policy, base, scheme string, checkLine func(path, line, sch string) bool, add func(*FieldError)) {
	switch {
	case p.Rule == RuleLatest && p.Count == 0:
		add(fieldErrorf(base+".count", "is required when rule is %q", RuleLatest))
	case p.Rule != RuleLatest && p.Count != 0:
		add(fieldErrorf(base+".count", "is only allowed when rule is %q", RuleLatest))
	}
	if p.Current && p.Rule != RuleLTS {
		add(fieldErrorf(base+".current", "is only allowed when rule is %q", RuleLTS))
	}
	if p.Admit == AdmitExplicit && p.Ceiling == "" {
		add(fieldErrorf(base+".ceiling", "is required when admit is %q", AdmitExplicit))
	}
	floorOK := p.Floor == "" || checkLine(base+".floor", p.Floor, scheme)
	ceilingOK := p.Ceiling == "" || checkLine(base+".ceiling", p.Ceiling, scheme)
	if p.Floor != "" && p.Ceiling != "" && floorOK && ceilingOK && release.CompareLines(p.Floor, p.Ceiling) > 0 {
		add(fieldErrorf(base+".floor", "%q is above ceiling %q", p.Floor, p.Ceiling))
	}
	validateLabels(p.Labels, base+".labels", scheme, add)
}

// validateLabels rejects a label that reads as a version, under the
// tool's scheme or as a major line, since it would sit in the matrix
// beside the release lines, and a label repeated within one policy.
func validateLabels(labels []string, base, scheme string, add func(*FieldError)) {
	seen := make(map[string]int, len(labels))
	for i, l := range labels {
		path := fmt.Sprintf("%s[%d]", base, i)
		if prev, dup := seen[l]; dup {
			add(fieldErrorf(path, "%q duplicates labels[%d]", l, prev))
			continue
		}
		seen[l] = i
		for _, sch := range []string{scheme, release.SchemeMajor} {
			if _, err := release.Parse(sch, l); err == nil {
				add(fieldErrorf(path, "%q reads as a version, not a label", l))
				break
			}
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
