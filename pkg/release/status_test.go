// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package release

import (
	"testing"
	"time"
)

// day parses a YYYY-MM-DD date as midnight UTC.
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
	d := day(t, s)
	return &d
}

// futureLine is a line that the 2026-10-28 feeds do not yet list.
const futureLine = "3.16"

func boolPtr(b bool) *bool { return &b }

// liveFacts returns release facts as the live feeds published them on
// 2026-10-28.
func liveFacts(t *testing.T) map[string]Facts {
	t.Helper()
	return map[string]Facts{
		"python 3.10": {
			Line:     lineThreeTen,
			Released: datePtr(t, "2021-10-04"),
			IsEOAS:   boolPtr(true),
			EOASFrom: datePtr(t, "2023-04-05"),
			EOLFrom:  datePtr(t, "2026-10-01"),
		},
		"python 3.13": {
			Line:     "3.13",
			Released: datePtr(t, "2024-10-07"),
			EOASFrom: datePtr(t, "2026-10-01"),
			EOLFrom:  datePtr(t, "2029-10-31"),
		},
		"python 3.15": {
			Line:     "3.15",
			Released: datePtr(t, "2026-10-01"),
			EOASFrom: datePtr(t, "2028-10-01"),
			EOLFrom:  datePtr(t, "2031-10-31"),
		},
		"nodejs 20": {
			Line:     "20",
			Released: datePtr(t, "2023-04-18"),
			LTS:      boolPtr(true),
			IsEOAS:   boolPtr(true),
			EOASFrom: datePtr(t, "2024-10-22"),
			IsEOL:    boolPtr(true),
			EOLFrom:  datePtr(t, "2026-04-30"),
			IsEOES:   boolPtr(false),
		},
		"gradle 8": {
			Line:     "8",
			Released: datePtr(t, "2023-02-13"),
			IsEOAS:   boolPtr(true),
		},
	}
}

func TestStatusOf(t *testing.T) {
	live := liveFacts(t)
	feedDay := day(t, "2026-10-28")
	tests := []struct {
		name  string
		facts Facts
		now   time.Time
		want  Status
	}{
		{"python 3.10 the day before EOL", live["python 3.10"], day(t, "2026-09-30"), StatusSecurity},
		{"python 3.10 late on the day before EOL", live["python 3.10"],
			time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC), StatusSecurity},
		{"python 3.10 on its EOL day", live["python 3.10"], day(t, "2026-10-01"), StatusEOL},
		{"python 3.10 on its EOL day in UTC, the day before locally", live["python 3.10"],
			time.Date(2026, 9, 30, 20, 0, 0, 0, time.FixedZone("UTC-5", -5*60*60)), StatusEOL},
		{"python 3.15", live["python 3.15"], feedDay, StatusActive},
		{"python 3.13 after EOAS", live["python 3.13"], feedDay, StatusSecurity},
		{"python 3.13 before EOAS", live["python 3.13"], day(t, "2026-09-30"), StatusActive},
		{"nodejs 20 with extended support", live["nodejs 20"], feedDay, StatusExtended},
		{"gradle 8 with EOAS flag only", live["gradle 8"], feedDay, StatusSecurity},
		{"extended support ended", Facts{
			Line:     "16",
			EOLFrom:  datePtr(t, "2023-09-11"),
			EOESFrom: datePtr(t, "2026-01-01"),
		}, feedDay, StatusEOL},
		{"extended support ends today", Facts{
			Line:     "16",
			EOLFrom:  datePtr(t, "2023-09-11"),
			EOESFrom: &feedDay,
		}, feedDay, StatusEOL},
		{"extended support until a future date", Facts{
			Line:     "18",
			IsEOL:    boolPtr(true),
			EOESFrom: datePtr(t, "2027-04-30"),
			IsEOES:   boolPtr(true),
		}, feedDay, StatusExtended},
		{"end-of-life with unknown extended support", Facts{
			Line:  "14",
			IsEOL: boolPtr(true),
		}, feedDay, StatusEOL},
		{"end-of-life with extended support ended", Facts{
			Line:   "14",
			IsEOL:  boolPtr(true),
			IsEOES: boolPtr(true),
		}, feedDay, StatusEOL},
		{"released after today", Facts{
			Line:     futureLine,
			Released: datePtr(t, "2027-10-01"),
		}, feedDay, StatusPrerelease},
		{"released today", Facts{
			Line:     futureLine,
			Released: &feedDay,
		}, feedDay, StatusActive},
		{"vendor pre-release flag", Facts{
			Line:       futureLine,
			Released:   datePtr(t, "2026-05-07"),
			Prerelease: true,
		}, feedDay, StatusPrerelease},
		{"no dates known", Facts{Line: "1"}, feedDay, StatusActive},
	}
	for _, tt := range tests {
		if got := StatusOf(tt.facts, tt.now); got != tt.want {
			t.Errorf("%s: StatusOf = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestLookup(t *testing.T) {
	live := liveFacts(t)
	facts := []Facts{live["python 3.15"], live["python 3.13"], live["python 3.10"]}
	now := day(t, "2026-10-28")

	f, got := Lookup(facts, "3.13", now)
	if got != StatusSecurity || f.Line != "3.13" {
		t.Errorf("Lookup(3.13) = %q, %q; want %q, %q", f.Line, got, "3.13", StatusSecurity)
	}
	f, got = Lookup(facts, lineThreeTen, now)
	if got != StatusEOL || f.Line != lineThreeTen {
		t.Errorf("Lookup(%s) = %q, %q; want %q, %q", lineThreeTen, f.Line, got, lineThreeTen, StatusEOL)
	}
	f, got = Lookup(facts, "3.1", now)
	if got != StatusUnknown || f.Line != "" {
		t.Errorf("Lookup(3.1) = %q, %q; want zero facts, %q", f.Line, got, StatusUnknown)
	}
	if _, got = Lookup(nil, "3.13", now); got != StatusUnknown {
		t.Errorf("Lookup on no facts = %q, want %q", got, StatusUnknown)
	}
}

func TestDaysToEOL(t *testing.T) {
	python310 := liveFacts(t)["python 3.10"]
	tests := []struct {
		name   string
		facts  Facts
		now    time.Time
		days   int
		wantOK bool
	}{
		{"before", python310, day(t, "2026-09-30"), 1, true},
		{"well before", python310, day(t, "2026-01-01"), 273, true},
		{"late the day before", python310, time.Date(2026, 9, 30, 23, 59, 59, 0, time.UTC), 1, true},
		{"on", python310, day(t, "2026-10-01"), 0, true},
		{"after", python310, day(t, "2026-10-28"), -27, true},
		{"without eolFrom", Facts{Line: "8", IsEOAS: boolPtr(true)}, day(t, "2026-10-28"), 0, false},
	}
	for _, tt := range tests {
		days, ok := DaysToEOL(tt.facts, tt.now)
		if days != tt.days || ok != tt.wantOK {
			t.Errorf("%s: DaysToEOL = %d, %v; want %d, %v", tt.name, days, ok, tt.days, tt.wantOK)
		}
	}
}
