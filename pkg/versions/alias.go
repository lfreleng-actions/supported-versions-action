// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// resolveAlias maps an nvm alias to a constraint on one release line,
// using the lifecycle facts: "node" and "stable" name the newest
// released line, "lts/*" the newest released LTS line and "lts/-N"
// the LTS line N before it. A codename alias such as "lts/jod" is
// unresolved, because the facts carry no codenames. An alias that
// cannot be resolved returns a nil constraint and an error.
func resolveAlias(scheme string, all []release.Facts, alias string, now time.Time) (*constraint.Constraint, error) {
	var lines, lts []string
	for _, f := range all {
		if f.Prerelease || f.Released == nil || f.Released.After(now) {
			continue
		}
		lines = append(lines, f.Line)
		if f.LTS != nil && *f.LTS {
			lts = append(lts, f.Line)
		}
	}
	newestFirst := func(a, b string) int { return release.CompareLines(b, a) }
	slices.SortFunc(lines, newestFirst)
	slices.SortFunc(lts, newestFirst)

	pick := func(from []string, back int) (string, error) {
		if back >= len(from) {
			return "", fmt.Errorf("alias %q names no release line the facts know", alias)
		}
		return from[back], nil
	}
	var (
		line string
		err  error
	)
	switch rest, isLTS := strings.CutPrefix(alias, "lts/"); {
	case alias == "node", alias == "stable":
		line, err = pick(lines, 0)
	case isLTS && rest == "*":
		line, err = pick(lts, 0)
	case isLTS && strings.HasPrefix(rest, "-"):
		n, convErr := strconv.Atoi(rest[1:])
		if convErr != nil || n < 1 {
			return nil, fmt.Errorf("alias %q is not a valid nvm alias", alias)
		}
		line, err = pick(lts, n)
	case isLTS:
		return nil, fmt.Errorf("alias %q is unresolved: the release facts carry no LTS codenames", alias)
	default:
		return nil, fmt.Errorf("alias %q is not a known nvm alias", alias)
	}
	if err != nil {
		return nil, err
	}
	c, err := constraint.NewLineSet(scheme, []string{line})
	if err != nil {
		return nil, fmt.Errorf("alias %q: %w", alias, err)
	}
	return &c, nil
}
