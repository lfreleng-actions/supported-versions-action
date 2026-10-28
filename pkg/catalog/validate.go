// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package catalog

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var linePatterns = map[string]*regexp.Regexp{
	SchemeMajor:      regexp.MustCompile(`^(0|[1-9][0-9]*)$`),
	SchemeMajorMinor: regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`),
}

// validate applies the checks the JSON Schema cannot express and
// returns every problem found, joined.
func (c *Catalog) validate() error {
	var errs []error
	add := func(fe *FieldError) { errs = append(errs, fe) }

	owners := make(map[string]string, len(c.Tools))
	for id := range c.Tools {
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
			if !validLine(line, sch) {
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
				if r.Min != "" && r.Max != "" && minOK && maxOK && compareLines(r.Min, r.Max) > 0 {
					add(fieldErrorf(lp+".min", "%q is above max %q", r.Min, r.Max))
				}
			}
		}
	}
	return errors.Join(errs...)
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
	if p.Floor != "" && p.Ceiling != "" && floorOK && ceilingOK && compareLines(p.Floor, p.Ceiling) > 0 {
		add(fieldErrorf(base+".floor", "%q is above ceiling %q", p.Floor, p.Ceiling))
	}
}

func validLine(line, scheme string) bool {
	re, ok := linePatterns[scheme]
	return ok && re.MatchString(line)
}

// compareLines compares two well-formed release lines numerically, so
// that a minor of 9 sorts before a minor of 10. Components carry no
// leading zeros, so comparing their digit strings by length and then
// lexically orders them numerically at any size, with no overflow.
func compareLines(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := "0", "0"
		if i < len(as) {
			x = as[i]
		}
		if i < len(bs) {
			y = bs[i]
		}
		if c := cmp.Compare(len(x), len(y)); c != 0 {
			return c
		}
		if c := strings.Compare(x, y); c != 0 {
			return c
		}
	}
	return 0
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
