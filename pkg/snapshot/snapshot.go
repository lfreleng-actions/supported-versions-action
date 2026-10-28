// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package snapshot holds the offline snapshot: a committed copy of the
// release facts the catalog's sources returned, embedded in the binary
// and used when neither the network nor the cache can answer.
//
// The snapshot stores normalised data rather than raw response bodies:
// the endoflife.date facts of each product, and the newest stable and
// pre-release build of each line of each versions manifest. It is
// small, and regenerating it produces diffs a reviewer can read.
package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	catalogdata "github.com/lfreleng-actions/supported-versions-action/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
)

// Schema is the snapshot format version this package reads and writes.
const Schema = 1

// MaxAge is how old a snapshot may be before Warning reports it.
const MaxAge = 30 * 24 * time.Hour

// Source types the snapshot covers. Other catalog source types are
// skipped.
const (
	// TypeEndOfLife is the catalog source type of endoflife.date.
	TypeEndOfLife = "endoflife"
	// TypeActionsManifest is the catalog source type of a versions
	// manifest of actions/setup-*.
	TypeActionsManifest = "actions-manifest"
)

const day = 24 * time.Hour

// Snapshot is the offline copy of the release facts of every
// endoflife and actions-manifest source in the catalog.
type Snapshot struct {
	// Schema is the format version, always Schema.
	Schema int
	// GeneratedAt is when the snapshot was generated, in UTC, to the
	// second.
	GeneratedAt time.Time
	// EndOfLife maps an endoflife.date product to its facts.
	EndOfLife map[string]Product
	// ActionsManifest maps an owner/name repository to the lines of
	// its versions manifest.
	ActionsManifest map[string]Manifest
}

// Product is the snapshot of one endoflife.date product.
type Product struct {
	// FetchedAt is when the facts were fetched, in UTC, to the second.
	FetchedAt time.Time
	// Releases are the product's release lines in ascending line
	// order.
	Releases []release.Facts
}

// Manifest is the snapshot of one versions manifest.
type Manifest struct {
	// FetchedAt is when the manifest was fetched, in UTC, to the
	// second.
	FetchedAt time.Time
	// Lines maps each release line to its newest builds.
	Lines map[string]actionsmanifest.Line
}

// Embedded parses the snapshot embedded in the binary.
func Embedded() (*Snapshot, error) {
	return Parse(catalogdata.Snapshot)
}

// Facts returns the release facts of an endoflife.date product and
// when they were fetched. found is false when the snapshot has no
// entry for product.
func (s *Snapshot) Facts(product string) (facts []release.Facts, fetchedAt time.Time, found bool) {
	p, ok := s.EndOfLife[product]
	if !ok {
		return nil, time.Time{}, false
	}
	return slices.Clone(p.Releases), p.FetchedAt, true
}

// Lines returns the lines of the versions manifest of repository and
// when it was fetched. found is false when the snapshot has no entry
// for repository.
func (s *Snapshot) Lines(repository string) (lines map[string]actionsmanifest.Line, fetchedAt time.Time, found bool) {
	m, ok := s.ActionsManifest[repository]
	if !ok {
		return nil, time.Time{}, false
	}
	return maps.Clone(m.Lines), m.FetchedAt, true
}

// Age returns how long before now the snapshot was generated.
func (s *Snapshot) Age(now time.Time) time.Duration {
	return now.Sub(s.GeneratedAt)
}

// Warning returns "" while the snapshot is no older than MaxAge, and
// otherwise a message naming the day it was generated and its age in
// whole days.
func (s *Snapshot) Warning(now time.Time) string {
	age := s.Age(now)
	if age <= MaxAge {
		return ""
	}
	return fmt.Sprintf("offline release data from %s is %d days old; it may miss recent releases and end-of-life dates",
		s.GeneratedAt.UTC().Format(time.DateOnly), int(age/day))
}

// wireSnapshot is the JSON form of a Snapshot.
type wireSnapshot struct {
	Schema          int                     `json:"schema"`
	GeneratedAt     string                  `json:"generated_at"`
	EndOfLife       map[string]wireProduct  `json:"endoflife"`
	ActionsManifest map[string]wireManifest `json:"actions_manifest"`
}

type wireProduct struct {
	FetchedAt string        `json:"fetched_at"`
	Releases  []wireRelease `json:"releases"`
}

// wireRelease is the JSON form of release.Facts. An unknown fact is a
// nil pointer, which omitempty leaves out.
type wireRelease struct {
	Line       string  `json:"line"`
	Released   *string `json:"released,omitempty"`
	LTS        *bool   `json:"lts,omitempty"`
	IsEOAS     *bool   `json:"is_eoas,omitempty"`
	EOASFrom   *string `json:"eoas_from,omitempty"`
	IsEOL      *bool   `json:"is_eol,omitempty"`
	EOLFrom    *string `json:"eol_from,omitempty"`
	IsEOES     *bool   `json:"is_eoes,omitempty"`
	EOESFrom   *string `json:"eoes_from,omitempty"`
	Latest     string  `json:"latest,omitempty"`
	LatestDate *string `json:"latest_date,omitempty"`
	Prerelease bool    `json:"prerelease,omitempty"`
}

type wireManifest struct {
	FetchedAt string              `json:"fetched_at"`
	Lines     map[string]wireLine `json:"lines"`
}

type wireLine struct {
	NewestStable     string `json:"newest_stable,omitempty"`
	NewestPrerelease string `json:"newest_prerelease,omitempty"`
}

// Marshal encodes the snapshot as JSON with sorted keys, two-space
// indentation and a trailing newline, so that encoding unchanged data
// gives identical bytes.
func (s *Snapshot) Marshal() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	w := wireSnapshot{
		Schema:          s.Schema,
		GeneratedAt:     formatTime(s.GeneratedAt),
		EndOfLife:       make(map[string]wireProduct, len(s.EndOfLife)),
		ActionsManifest: make(map[string]wireManifest, len(s.ActionsManifest)),
	}
	for name, p := range s.EndOfLife {
		releases := slices.Clone(p.Releases)
		slices.SortStableFunc(releases, func(a, b release.Facts) int {
			return release.CompareLines(a.Line, b.Line)
		})
		wp := wireProduct{FetchedAt: formatTime(p.FetchedAt), Releases: make([]wireRelease, 0, len(releases))}
		for _, f := range releases {
			wp.Releases = append(wp.Releases, toWire(f))
		}
		w.EndOfLife[name] = wp
	}
	for repo, m := range s.ActionsManifest {
		wm := wireManifest{FetchedAt: formatTime(m.FetchedAt), Lines: make(map[string]wireLine, len(m.Lines))}
		for line, l := range m.Lines {
			wm.Lines[line] = wireLine{NewestStable: l.NewestStable, NewestPrerelease: l.NewestPrerelease}
		}
		w.ActionsManifest[repo] = wm
	}
	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	return append(data, '\n'), nil
}

// Parse decodes and validates a snapshot. It rejects any schema other
// than Schema, unknown and duplicate keys, malformed dates and
// timestamps, and duplicate lines.
func Parse(data []byte) (*Snapshot, error) {
	if err := checkDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w wireSnapshot
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("snapshot: unexpected data after the snapshot object")
	}
	if w.Schema != Schema {
		return nil, fmt.Errorf("snapshot: unsupported schema %d, want %d", w.Schema, Schema)
	}
	s := &Snapshot{
		Schema:          w.Schema,
		EndOfLife:       make(map[string]Product, len(w.EndOfLife)),
		ActionsManifest: make(map[string]Manifest, len(w.ActionsManifest)),
	}
	var err error
	if s.GeneratedAt, err = parseTime("generated_at", w.GeneratedAt); err != nil {
		return nil, err
	}
	for name, wp := range w.EndOfLife {
		p := Product{Releases: make([]release.Facts, 0, len(wp.Releases))}
		if p.FetchedAt, err = parseTime("endoflife."+name+".fetched_at", wp.FetchedAt); err != nil {
			return nil, err
		}
		for _, wr := range wp.Releases {
			f, ferr := fromWire(wr)
			if ferr != nil {
				return nil, fmt.Errorf("snapshot: endoflife.%s: %w", name, ferr)
			}
			p.Releases = append(p.Releases, f)
		}
		slices.SortStableFunc(p.Releases, func(a, b release.Facts) int {
			return release.CompareLines(a.Line, b.Line)
		})
		s.EndOfLife[name] = p
	}
	for repo, wm := range w.ActionsManifest {
		m := Manifest{Lines: make(map[string]actionsmanifest.Line, len(wm.Lines))}
		if m.FetchedAt, err = parseTime("actions_manifest."+repo+".fetched_at", wm.FetchedAt); err != nil {
			return nil, err
		}
		for line, wl := range wm.Lines {
			m.Lines[line] = actionsmanifest.Line{NewestStable: wl.NewestStable, NewestPrerelease: wl.NewestPrerelease}
		}
		s.ActionsManifest[repo] = m
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// validate checks what the JSON form cannot express: the schema, and
// that every line is named and appears once per entry.
func (s *Snapshot) validate() error {
	if s.Schema != Schema {
		return fmt.Errorf("snapshot: unsupported schema %d, want %d", s.Schema, Schema)
	}
	for _, name := range slices.Sorted(maps.Keys(s.EndOfLife)) {
		seen := make([]string, 0, len(s.EndOfLife[name].Releases))
		for _, f := range s.EndOfLife[name].Releases {
			if f.Line == "" {
				return fmt.Errorf("snapshot: endoflife.%s: release without a line", name)
			}
			if slices.ContainsFunc(seen, func(l string) bool { return release.CompareLines(l, f.Line) == 0 }) {
				return fmt.Errorf("snapshot: endoflife.%s: duplicate line %q", name, f.Line)
			}
			seen = append(seen, f.Line)
		}
	}
	for _, repo := range slices.Sorted(maps.Keys(s.ActionsManifest)) {
		if _, ok := s.ActionsManifest[repo].Lines[""]; ok {
			return fmt.Errorf("snapshot: actions_manifest.%s: empty line", repo)
		}
	}
	return nil
}

func toWire(f release.Facts) wireRelease {
	return wireRelease{
		Line:       f.Line,
		Released:   formatDate(f.Released),
		LTS:        f.LTS,
		IsEOAS:     f.IsEOAS,
		EOASFrom:   formatDate(f.EOASFrom),
		IsEOL:      f.IsEOL,
		EOLFrom:    formatDate(f.EOLFrom),
		IsEOES:     f.IsEOES,
		EOESFrom:   formatDate(f.EOESFrom),
		Latest:     f.Latest,
		LatestDate: formatDate(f.LatestDate),
		Prerelease: f.Prerelease,
	}
}

func fromWire(w wireRelease) (release.Facts, error) {
	if w.Line == "" {
		return release.Facts{}, errors.New("release without a line")
	}
	f := release.Facts{
		Line:       w.Line,
		LTS:        w.LTS,
		IsEOAS:     w.IsEOAS,
		IsEOL:      w.IsEOL,
		IsEOES:     w.IsEOES,
		Latest:     w.Latest,
		Prerelease: w.Prerelease,
	}
	dates := []struct {
		field string
		in    *string
		out   **time.Time
	}{
		{"released", w.Released, &f.Released},
		{"eoas_from", w.EOASFrom, &f.EOASFrom},
		{"eol_from", w.EOLFrom, &f.EOLFrom},
		{"eoes_from", w.EOESFrom, &f.EOESFrom},
		{"latest_date", w.LatestDate, &f.LatestDate},
	}
	for _, d := range dates {
		if d.in == nil {
			continue
		}
		t, err := time.Parse(time.DateOnly, *d.in)
		if err != nil {
			return release.Facts{}, fmt.Errorf("line %q: %s %q is not a YYYY-MM-DD date", w.Line, d.field, *d.in)
		}
		*d.out = &t
	}
	return f, nil
}

func formatDate(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.DateOnly)
	return &s
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// parseTime parses an RFC 3339 timestamp in UTC with no fractional
// seconds, the form formatTime writes.
func parseTime(field, s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || formatTime(t) != s {
		return time.Time{}, fmt.Errorf("snapshot: %s %q is not a timestamp of the form YYYY-MM-DDTHH:MM:SSZ", field, s)
	}
	return t.UTC(), nil
}

// stamp returns t in UTC, truncated to the second, the precision the
// snapshot stores.
func stamp(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// checkDuplicateKeys reports the first object key that appears twice
// in one JSON object, which encoding/json would otherwise accept by
// keeping the last value.
func checkDuplicateKeys(data []byte) error {
	type frame struct {
		keys      map[string]bool
		object    bool
		expectKey bool
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var stack []*frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
			top := stack[n-1]
			key, ok := tok.(string)
			if !ok {
				// The only other token here is the closing brace.
				stack = stack[:n-1]
				valueDone()
				continue
			}
			if top.keys[key] {
				return fmt.Errorf("duplicate key %q", key)
			}
			top.keys[key] = true
			top.expectKey = false
			continue
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{keys: make(map[string]bool), object: true, expectKey: true})
		case json.Delim('['):
			stack = append(stack, &frame{})
		case json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
}
