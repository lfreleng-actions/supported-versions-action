// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package release

import "time"

// Status is where a release line stands in its support life cycle.
type Status string

// Release line statuses.
const (
	// StatusPrerelease is a line not yet released, or one its vendor
	// marks as a release candidate or similar.
	StatusPrerelease Status = "prerelease"
	// StatusActive is a released line still in active support.
	StatusActive Status = "active"
	// StatusSecurity is a line past active support that still gets
	// security fixes.
	StatusSecurity Status = "security"
	// StatusExtended is a line past end-of-life that still has
	// extended, usually paid, support.
	StatusExtended Status = "extended"
	// StatusEOL is a line past end-of-life with no extended support.
	StatusEOL Status = "eol"
	// StatusUnknown is a line that no source lists.
	StatusUnknown Status = "unknown"
)

// Facts are the dated release facts for one release line, mirroring
// the release fields of the endoflife.date v1 API. A nil field means
// the fact is unknown. Dates name calendar days in UTC.
type Facts struct {
	// Line is the release line (endoflife.date "name").
	Line string
	// Released is the release date ("releaseDate").
	Released *time.Time
	// LTS reports a long-term support line ("isLts").
	LTS *bool
	// IsEOAS reports that active support has ended ("isEoas").
	IsEOAS *bool
	// EOASFrom is the day active support ends ("eoasFrom").
	EOASFrom *time.Time
	// IsEOL reports that the line is end-of-life ("isEol").
	IsEOL *bool
	// EOLFrom is the day the line reaches end-of-life ("eolFrom").
	EOLFrom *time.Time
	// IsEOES reports that extended support has ended ("isEoes").
	IsEOES *bool
	// EOESFrom is the day extended support ends ("eoesFrom").
	EOESFrom *time.Time
	// Latest is the newest patch release of the line ("latest.name").
	// It is empty when unknown. StatusOf does not read it.
	Latest string
	// LatestDate is the day Latest was released ("latest.date").
	// StatusOf does not read it.
	LatestDate *time.Time
	// Prerelease reports that the vendor marks the line as a release
	// candidate or similar.
	Prerelease bool
}

// StatusOf decides the status of the line f describes on the UTC
// calendar day of now. Status comes from the dates and the vendor's
// end-of-support flags, never from a "maintained" flag. A date has
// passed when today is on or after it. The checks run in order:
//
//  1. StatusPrerelease when Released is after today or f.Prerelease
//     is set.
//  2. When EOLFrom has passed or IsEOL is true: StatusExtended when
//     EOESFrom is set and has not passed, or is unset and IsEOES is
//     explicitly false; otherwise StatusEOL.
//  3. StatusSecurity when EOASFrom has passed or IsEOAS is true.
//  4. StatusActive otherwise, including when no dates are known.
func StatusOf(f Facts, now time.Time) Status {
	today := utcDay(now)
	switch {
	case f.Prerelease || (f.Released != nil && utcDay(*f.Released).After(today)):
		return StatusPrerelease
	case passed(f.EOLFrom, today) || isTrue(f.IsEOL):
		if f.EOESFrom != nil {
			if !passed(f.EOESFrom, today) {
				return StatusExtended
			}
		} else if f.IsEOES != nil && !*f.IsEOES {
			return StatusExtended
		}
		return StatusEOL
	case passed(f.EOASFrom, today) || isTrue(f.IsEOAS):
		return StatusSecurity
	default:
		return StatusActive
	}
}

// Lookup finds line among facts, comparing lines with CompareLines, and
// returns its facts and status on the day of now. A line no entry
// lists returns zero Facts and StatusUnknown.
func Lookup(facts []Facts, line string, now time.Time) (Facts, Status) {
	for _, f := range facts {
		if CompareLines(f.Line, line) == 0 {
			return f, StatusOf(f, now)
		}
	}
	return Facts{}, StatusUnknown
}

// DaysToEOL returns the whole days from the UTC calendar day of now
// until EOLFrom: zero on the day itself and negative once it has
// passed. ok is false when EOLFrom is unknown.
func DaysToEOL(f Facts, now time.Time) (days int, ok bool) {
	if f.EOLFrom == nil {
		return 0, false
	}
	return int(utcDay(*f.EOLFrom).Sub(utcDay(now)) / (24 * time.Hour)), true
}

// utcDay returns midnight UTC at the start of t's UTC calendar day.
func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func passed(d *time.Time, today time.Time) bool {
	return d != nil && !today.Before(utcDay(*d))
}

func isTrue(b *bool) bool {
	return b != nil && *b
}
