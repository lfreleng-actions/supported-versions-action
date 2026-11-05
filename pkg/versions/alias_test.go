// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

const (
	errCodenames = "codenames"
	errInvalid   = "not a valid"
)

func TestResolveAlias(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	day := func(y int, m time.Month) *time.Time {
		d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		return &d
	}
	yes, no := true, false
	// Node.js as it stood in early 2026, with a line not yet released
	// and one without a release date.
	all := []release.Facts{
		{Line: "26", Released: day(2026, 4), LTS: &no},
		{Line: "25", Released: day(2025, 10), LTS: &no},
		{Line: "24", Released: day(2025, 5), LTS: &yes},
		{Line: "23", Released: day(2024, 10), LTS: &no},
		{Line: "22", Released: day(2024, 4), LTS: &yes},
		{Line: "20", Released: day(2023, 4), LTS: &yes},
		{Line: "27", LTS: &yes},
	}
	lines := []string{"20", "22", "23", "24", "25", "26"}
	for alias, want := range map[string]string{
		"lts/*":  "24",
		"lts/-1": "22",
		"lts/-2": "20",
		"node":   "25",
		"stable": "25",
	} {
		c, err := resolveAlias("major", all, alias, now)
		if err != nil {
			t.Errorf("%s: %v", alias, err)
			continue
		}
		var got []string
		for _, l := range lines {
			if c.Allows(l) {
				got = append(got, l)
			}
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s allows %v, want [%s]", alias, got, want)
		}
	}

	for alias, want := range map[string]string{
		"lts/jod":  errCodenames,
		"lts/-3":   "names no release line",
		"lts/-0":   errInvalid,
		"lts/-x":   errInvalid,
		"lts/--1":  errInvalid,
		"lts/":     errCodenames,
		"iojs":     "not a known",
		"lts/hydr": errCodenames,
	} {
		c, err := resolveAlias("major", all, alias, now)
		if c != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, %v; want an error containing %q", alias, c, err, want)
		}
	}

	if c, err := resolveAlias("major", nil, "lts/*", now); c != nil || err == nil {
		t.Errorf("no facts: %v, %v; want an error", c, err)
	}
}
