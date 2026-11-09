// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package snapshot holds the offline snapshot: a committed copy of the
// release facts the catalog's sources returned, embedded in the binary
// and used when neither the network nor the cache can answer.
//
// The snapshot stores normalised data rather than raw response bodies:
// the endoflife.date facts of each product, the newest stable and
// pre-release build of each line of each versions manifest and each
// Maven Central artifact, the release lines Adoptium has builds of, and
// the newest Gradle release of each line with its checksum URL and the
// line's withdrawn releases, the release the Rust stable channel
// serves, and the newest releases of each line of each GitHub
// repository read through github-releases. It is small, and regenerating it produces
// diffs a reviewer can read.
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
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/githubreleases"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/gradleservices"
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
	// TypeAdoptium is the catalog source type of the Adoptium API.
	TypeAdoptium = "adoptium"
	// TypeMavenMetadata is the catalog source type of an artifact's
	// maven-metadata.xml on Maven Central.
	TypeMavenMetadata = "maven-metadata"
	// TypeGradleServices is the catalog source type of
	// services.gradle.org.
	TypeGradleServices = "gradle-services"
	// TypeRustChannel is the catalog source type of the Rust stable
	// channel manifest.
	TypeRustChannel = "rust-channel"
	// TypeGitHubReleases is the catalog source type of a GitHub
	// repository's releases.
	TypeGitHubReleases = githubreleases.Type
	// TypeStatic is the catalog source type of lines the catalog
	// holds. The snapshot has nothing to store for it.
	TypeStatic = "static"
)

const day = 24 * time.Hour

// Snapshot is the offline copy of the release facts of every
// endoflife, actions-manifest, adoptium, maven-metadata,
// gradle-services, rust-channel and github-releases source in the
// catalog.
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
	// Adoptium is the snapshot of the Adoptium API, or nil when the
	// catalog has no adoptium source.
	Adoptium *Adoptium
	// MavenMetadata maps a group:artifact coordinate to the lines of
	// its Maven Central metadata.
	MavenMetadata map[string]Manifest
	// GradleServices is the snapshot of services.gradle.org, or nil
	// when the catalog has no gradle-services source.
	GradleServices *GradleServices
	// RustChannel is the snapshot of the Rust stable channel, or nil
	// when the catalog has no rust-channel source.
	RustChannel *RustChannel
	// GitHubReleases maps an owner/name repository to the lines of
	// its GitHub releases.
	GitHubReleases map[string]GitHubRepo
}

// GitHubRepo is the snapshot of one repository's GitHub releases.
type GitHubRepo struct {
	// FetchedAt is when the releases were fetched, in UTC, to the
	// second.
	FetchedAt time.Time
	// Lines maps each release line to its first and newest releases.
	Lines map[string]githubreleases.Line
}

// RustChannel is the snapshot of the release the Rust stable channel
// serves.
type RustChannel struct {
	// FetchedAt is when the channel manifest was fetched, in UTC, to
	// the second.
	FetchedAt time.Time
	// Version is the stable release, such as "1.99.1".
	Version string
	// Date is the day the channel manifest was published.
	Date time.Time
}

// GradleServices is the snapshot of the final Gradle releases
// services.gradle.org lists.
type GradleServices struct {
	// FetchedAt is when the versions were fetched, in UTC, to the
	// second.
	FetchedAt time.Time
	// Lines maps each release line to its newest release and its
	// withdrawn releases.
	Lines map[string]gradleservices.Line
}

// Adoptium is the snapshot of the release lines Adoptium has builds of.
type Adoptium struct {
	// FetchedAt is when the releases were fetched, in UTC, to the
	// second.
	FetchedAt time.Time
	// Lines are the available release lines in ascending line order.
	Lines []string
}

// Product is the snapshot of one endoflife.date product.
type Product struct {
	// FetchedAt is when the facts were fetched, in UTC, to the second.
	FetchedAt time.Time
	// Releases are the product's release lines in ascending line
	// order.
	Releases []release.Facts
}

// Manifest is the snapshot of one versions manifest or one artifact's
// Maven Central metadata.
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

// MavenLines returns the lines of the Maven Central metadata of
// artifact and when it was fetched. found is false when the snapshot
// has no entry for artifact.
func (s *Snapshot) MavenLines(artifact string) (lines map[string]actionsmanifest.Line, fetchedAt time.Time, found bool) {
	m, ok := s.MavenMetadata[artifact]
	if !ok {
		return nil, time.Time{}, false
	}
	return maps.Clone(m.Lines), m.FetchedAt, true
}

// GitHubLines returns the lines of the GitHub releases of repository
// and when they were fetched. found is false when the snapshot has no
// entry for repository.
func (s *Snapshot) GitHubLines(repository string) (lines map[string]githubreleases.Line, fetchedAt time.Time, found bool) {
	r, ok := s.GitHubReleases[repository]
	if !ok {
		return nil, time.Time{}, false
	}
	return maps.Clone(r.Lines), r.FetchedAt, true
}

// GradleLines returns the newest Gradle release of each line, with
// the line's withdrawn releases, and when the versions were fetched.
// found is false when the snapshot has no gradle-services entry.
func (s *Snapshot) GradleLines() (lines map[string]gradleservices.Line, fetchedAt time.Time, found bool) {
	if s.GradleServices == nil {
		return nil, time.Time{}, false
	}
	return maps.Clone(s.GradleServices.Lines), s.GradleServices.FetchedAt, true
}

// RustStable returns the release the Rust stable channel serves, the
// day its manifest was published, and when it was fetched. found is
// false when the snapshot has no rust-channel entry.
func (s *Snapshot) RustStable() (version string, date, fetchedAt time.Time, found bool) {
	if s.RustChannel == nil {
		return "", time.Time{}, time.Time{}, false
	}
	return s.RustChannel.Version, s.RustChannel.Date, s.RustChannel.FetchedAt, true
}

// AdoptiumLines returns the release lines Adoptium has builds of and
// when they were fetched. found is false when the snapshot has no
// Adoptium entry.
func (s *Snapshot) AdoptiumLines() (lines []string, fetchedAt time.Time, found bool) {
	if s.Adoptium == nil {
		return nil, time.Time{}, false
	}
	return slices.Clone(s.Adoptium.Lines), s.Adoptium.FetchedAt, true
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
	Adoptium        *wireAdoptium           `json:"adoptium,omitempty"`
	MavenMetadata   map[string]wireManifest `json:"maven_metadata,omitempty"`
	GradleServices  *wireGradleServices     `json:"gradle_services,omitempty"`
	RustChannel     *wireRustChannel        `json:"rust_channel,omitempty"`
	GitHubReleases  map[string]wireGitHub   `json:"github_releases,omitempty"`
}

type wireGitHub struct {
	FetchedAt string                    `json:"fetched_at"`
	Lines     map[string]wireGitHubLine `json:"lines"`
}

// wireGitHubLine is one line of a repository's GitHub releases.
// Released is a day; the publication times are timestamps, so that a
// tie between two releases of one version published on one day breaks
// as it does on live data.
type wireGitHubLine struct {
	Released                  *string `json:"released,omitempty"`
	NewestStable              string  `json:"newest_stable,omitempty"`
	NewestStablePublished     *string `json:"newest_stable_published,omitempty"`
	NewestPrerelease          string  `json:"newest_prerelease,omitempty"`
	NewestPrereleasePublished *string `json:"newest_prerelease_published,omitempty"`
}

type wireRustChannel struct {
	FetchedAt string `json:"fetched_at"`
	Version   string `json:"version"`
	Date      string `json:"date"`
}

type wireGradleServices struct {
	FetchedAt string                    `json:"fetched_at"`
	Lines     map[string]wireGradleLine `json:"lines"`
}

type wireGradleLine struct {
	NewestStable string   `json:"newest_stable,omitempty"`
	ChecksumURL  string   `json:"checksum_url,omitempty"`
	Broken       []string `json:"broken,omitempty"`
}

type wireAdoptium struct {
	FetchedAt string   `json:"fetched_at"`
	Lines     []string `json:"lines"`
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
		w.ActionsManifest[repo] = m.toWire()
	}
	if len(s.MavenMetadata) > 0 {
		w.MavenMetadata = make(map[string]wireManifest, len(s.MavenMetadata))
		for artifact, m := range s.MavenMetadata {
			w.MavenMetadata[artifact] = m.toWire()
		}
	}
	if g := s.GradleServices; g != nil {
		w.GradleServices = &wireGradleServices{FetchedAt: formatTime(g.FetchedAt), Lines: make(map[string]wireGradleLine, len(g.Lines))}
		for line, l := range g.Lines {
			w.GradleServices.Lines[line] = wireGradleLine{NewestStable: l.NewestStable, ChecksumURL: l.ChecksumURL, Broken: l.Broken}
		}
	}
	if rc := s.RustChannel; rc != nil {
		w.RustChannel = &wireRustChannel{FetchedAt: formatTime(rc.FetchedAt), Version: rc.Version, Date: rc.Date.UTC().Format(time.DateOnly)}
	}
	if len(s.GitHubReleases) > 0 {
		w.GitHubReleases = make(map[string]wireGitHub, len(s.GitHubReleases))
		for repo, r := range s.GitHubReleases {
			wg := wireGitHub{FetchedAt: formatTime(r.FetchedAt), Lines: make(map[string]wireGitHubLine, len(r.Lines))}
			for line, l := range r.Lines {
				wg.Lines[line] = wireGitHubLine{
					Released:                  formatDate(l.Released),
					NewestStable:              l.NewestStable,
					NewestStablePublished:     formatTimePtr(l.NewestStablePublished),
					NewestPrerelease:          l.NewestPrerelease,
					NewestPrereleasePublished: formatTimePtr(l.NewestPrereleasePublished),
				}
			}
			w.GitHubReleases[repo] = wg
		}
	}
	if s.Adoptium != nil {
		w.Adoptium = &wireAdoptium{FetchedAt: formatTime(s.Adoptium.FetchedAt), Lines: sortedLines(s.Adoptium.Lines)}
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
		if s.ActionsManifest[repo], err = wm.fromWire("actions_manifest." + repo); err != nil {
			return nil, err
		}
	}
	if len(w.MavenMetadata) > 0 {
		s.MavenMetadata = make(map[string]Manifest, len(w.MavenMetadata))
		for artifact, wm := range w.MavenMetadata {
			if s.MavenMetadata[artifact], err = wm.fromWire("maven_metadata." + artifact); err != nil {
				return nil, err
			}
		}
	}
	if w.Adoptium != nil {
		a := &Adoptium{Lines: sortedLines(w.Adoptium.Lines)}
		if a.FetchedAt, err = parseTime("adoptium.fetched_at", w.Adoptium.FetchedAt); err != nil {
			return nil, err
		}
		s.Adoptium = a
	}
	if wg := w.GradleServices; wg != nil {
		g := &GradleServices{Lines: make(map[string]gradleservices.Line, len(wg.Lines))}
		if g.FetchedAt, err = parseTime("gradle_services.fetched_at", wg.FetchedAt); err != nil {
			return nil, err
		}
		for line, wl := range wg.Lines {
			g.Lines[line] = gradleservices.Line{NewestStable: wl.NewestStable, ChecksumURL: wl.ChecksumURL, Broken: wl.Broken}
		}
		s.GradleServices = g
	}
	if wr := w.RustChannel; wr != nil {
		rc := &RustChannel{Version: wr.Version}
		if rc.FetchedAt, err = parseTime("rust_channel.fetched_at", wr.FetchedAt); err != nil {
			return nil, err
		}
		if rc.Date, err = time.Parse(time.DateOnly, wr.Date); err != nil {
			return nil, fmt.Errorf("snapshot: rust_channel.date: %q is not a YYYY-MM-DD date", wr.Date)
		}
		s.RustChannel = rc
	}
	if s.GitHubReleases, err = gitHubFromWire(w.GitHubReleases); err != nil {
		return nil, err
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// gitHubFromWire decodes the github_releases entries; nil when there
// are none.
func gitHubFromWire(w map[string]wireGitHub) (map[string]GitHubRepo, error) {
	if len(w) == 0 {
		return nil, nil
	}
	out := make(map[string]GitHubRepo, len(w))
	for repo, wg := range w {
		r, err := wg.fromWire("github_releases." + repo)
		if err != nil {
			return nil, err
		}
		out[repo] = r
	}
	return out, nil
}

// fromWire decodes the repository's releases at the JSON path where.
func (wg wireGitHub) fromWire(where string) (GitHubRepo, error) {
	r := GitHubRepo{Lines: make(map[string]githubreleases.Line, len(wg.Lines))}
	var err error
	if r.FetchedAt, err = parseTime(where+".fetched_at", wg.FetchedAt); err != nil {
		return GitHubRepo{}, err
	}
	for line, wl := range wg.Lines {
		l := githubreleases.Line{NewestStable: wl.NewestStable, NewestPrerelease: wl.NewestPrerelease}
		if wl.Released != nil {
			t, perr := time.Parse(time.DateOnly, *wl.Released)
			if perr != nil {
				return GitHubRepo{}, fmt.Errorf("snapshot: %s: line %q: released %q is not a YYYY-MM-DD date", where, line, *wl.Released)
			}
			l.Released = &t
		}
		stamps := []struct {
			field string
			in    *string
			out   **time.Time
		}{
			{"newest_stable_published", wl.NewestStablePublished, &l.NewestStablePublished},
			{"newest_prerelease_published", wl.NewestPrereleasePublished, &l.NewestPrereleasePublished},
		}
		for _, d := range stamps {
			if d.in == nil {
				continue
			}
			t, perr := parseTime(fmt.Sprintf("%s: line %q: %s", where, line, d.field), *d.in)
			if perr != nil {
				return GitHubRepo{}, perr
			}
			*d.out = &t
		}
		r.Lines[line] = l
	}
	return r, nil
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
	for _, artifact := range slices.Sorted(maps.Keys(s.MavenMetadata)) {
		if _, ok := s.MavenMetadata[artifact].Lines[""]; ok {
			return fmt.Errorf("snapshot: maven_metadata.%s: empty line", artifact)
		}
	}
	if s.GradleServices != nil {
		for _, line := range slices.Sorted(maps.Keys(s.GradleServices.Lines)) {
			if line == "" {
				return errors.New("snapshot: gradle_services: empty line")
			}
			l := s.GradleServices.Lines[line]
			if l.NewestStable == "" && len(l.Broken) == 0 {
				return fmt.Errorf("snapshot: gradle_services: line %q has neither newest_stable nor broken", line)
			}
			if slices.Contains(l.Broken, "") {
				return fmt.Errorf("snapshot: gradle_services: line %q has an empty broken release", line)
			}
		}
	}
	if err := s.validateGitHub(); err != nil {
		return err
	}
	if s.RustChannel != nil && s.RustChannel.Version == "" {
		return errors.New("snapshot: rust_channel: empty version")
	}
	if s.Adoptium != nil {
		lines := sortedLines(s.Adoptium.Lines)
		for i, l := range lines {
			if l == "" {
				return errors.New("snapshot: adoptium: empty line")
			}
			if i > 0 && release.CompareLines(lines[i-1], l) == 0 {
				return fmt.Errorf("snapshot: adoptium: duplicate line %q", l)
			}
		}
	}
	return nil
}

// validateGitHub checks that every line of every repository's GitHub
// releases is named and has a newest release.
func (s *Snapshot) validateGitHub() error {
	for _, repo := range slices.Sorted(maps.Keys(s.GitHubReleases)) {
		for _, line := range slices.Sorted(maps.Keys(s.GitHubReleases[repo].Lines)) {
			if line == "" {
				return fmt.Errorf("snapshot: github_releases.%s: empty line", repo)
			}
			l := s.GitHubReleases[repo].Lines[line]
			if l.NewestStable == "" && l.NewestPrerelease == "" {
				return fmt.Errorf("snapshot: github_releases.%s: line %q has neither newest_stable nor newest_prerelease", repo, line)
			}
		}
	}
	return nil
}

func (m Manifest) toWire() wireManifest {
	wm := wireManifest{FetchedAt: formatTime(m.FetchedAt), Lines: make(map[string]wireLine, len(m.Lines))}
	for line, l := range m.Lines {
		wm.Lines[line] = wireLine{NewestStable: l.NewestStable, NewestPrerelease: l.NewestPrerelease}
	}
	return wm
}

// fromWire decodes the manifest at the JSON path where.
func (wm wireManifest) fromWire(where string) (Manifest, error) {
	m := Manifest{Lines: make(map[string]actionsmanifest.Line, len(wm.Lines))}
	var err error
	if m.FetchedAt, err = parseTime(where+".fetched_at", wm.FetchedAt); err != nil {
		return Manifest{}, err
	}
	for line, wl := range wm.Lines {
		m.Lines[line] = actionsmanifest.Line{NewestStable: wl.NewestStable, NewestPrerelease: wl.NewestPrerelease}
	}
	return m, nil
}

// sortedLines returns a copy of lines in ascending line order.
func sortedLines(lines []string) []string {
	out := slices.Clone(lines)
	if out == nil {
		out = []string{}
	}
	slices.SortStableFunc(out, release.CompareLines)
	return out
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

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
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
