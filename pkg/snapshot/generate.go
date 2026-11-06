// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package snapshot

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/adoptium"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/endoflife"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/gradleservices"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/mavenmeta"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/rustchannel"
)

// Generate fetches every endoflife, actions-manifest, adoptium,
// maven-metadata, gradle-services and rust-channel source of the
// catalog's tools, in
// cat.IDs() order, and returns their snapshot. Manifests, Maven
// metadata and Gradle versions are reduced to lines under the version
// scheme of the first tool naming them, and endoflife products keep
// only the lines valid under it. A source two tools share is fetched once. Other
// source types are skipped. Any fetch error fails the whole generation,
// so a partial snapshot is never returned. GeneratedAt is now(), or
// time.Now when now is nil.
func Generate(ctx context.Context, cat *catalog.Catalog, eol *endoflife.Client, manifests *actionsmanifest.Client, releases *adoptium.Client, maven *mavenmeta.Client, gradle *gradleservices.Client, rust *rustchannel.Client, now func() time.Time) (*Snapshot, error) {
	if now == nil {
		now = time.Now
	}
	s := &Snapshot{
		Schema:          Schema,
		EndOfLife:       make(map[string]Product),
		ActionsManifest: make(map[string]Manifest),
	}
	for _, id := range cat.IDs() {
		tool := cat.Tools[id]
		for _, src := range tool.Sources {
			switch src.Type {
			case TypeEndOfLife:
				if _, done := s.EndOfLife[src.Product]; done {
					continue
				}
				facts, res, err := eol.Releases(ctx, src.Product)
				if err != nil {
					return nil, fmt.Errorf("snapshot: tool %s: %w", id, err)
				}
				s.EndOfLife[src.Product] = Product{
					FetchedAt: stamp(res.FetchedAt),
					Releases:  release.OnScheme(facts, tool.VersionScheme),
				}
			case TypeActionsManifest:
				if _, done := s.ActionsManifest[src.Repository]; done {
					continue
				}
				builds, res, err := manifests.Builds(ctx, src.Repository)
				if err != nil {
					return nil, fmt.Errorf("snapshot: tool %s: %w", id, err)
				}
				s.ActionsManifest[src.Repository] = Manifest{
					FetchedAt: stamp(res.FetchedAt),
					Lines:     actionsmanifest.Lines(builds, tool.VersionScheme),
				}
			case TypeAdoptium:
				if s.Adoptium != nil {
					continue
				}
				r, res, err := releases.Releases(ctx)
				if err != nil {
					return nil, fmt.Errorf("snapshot: tool %s: %w", id, err)
				}
				s.Adoptium = &Adoptium{FetchedAt: stamp(res.FetchedAt), Lines: r.Lines()}
			case TypeGradleServices:
				if s.GradleServices != nil {
					continue
				}
				versions, res, err := gradle.Versions(ctx)
				if err != nil {
					return nil, fmt.Errorf("snapshot: tool %s: %w", id, err)
				}
				s.GradleServices = &GradleServices{
					FetchedAt: stamp(res.FetchedAt),
					Lines:     gradleservices.Lines(versions, tool.VersionScheme),
				}
			case TypeRustChannel:
				if s.RustChannel != nil {
					continue
				}
				stable, res, err := rust.Stable(ctx)
				if err != nil {
					return nil, fmt.Errorf("snapshot: tool %s: %w", id, err)
				}
				s.RustChannel = &RustChannel{FetchedAt: stamp(res.FetchedAt), Version: stable.Version, Date: stable.Date}
			case TypeMavenMetadata:
				if _, done := s.MavenMetadata[src.Artifact]; done {
					continue
				}
				versions, res, err := maven.Versions(ctx, src.Artifact)
				if err != nil {
					return nil, fmt.Errorf("snapshot: tool %s: %w", id, err)
				}
				if s.MavenMetadata == nil {
					s.MavenMetadata = make(map[string]Manifest)
				}
				s.MavenMetadata[src.Artifact] = Manifest{
					FetchedAt: stamp(res.FetchedAt),
					Lines:     mavenLines(versions, tool.VersionScheme),
				}
			}
		}
	}
	s.GeneratedAt = stamp(now())
	return s, nil
}

// mavenLines maps Maven versions to their lines under scheme, in the
// form the snapshot stores versions manifest lines in.
func mavenLines(versions []string, scheme string) map[string]actionsmanifest.Line {
	lines := make(map[string]actionsmanifest.Line)
	for l, b := range mavenmeta.Lines(versions, scheme) {
		lines[l] = actionsmanifest.Line{NewestStable: b.NewestStable, NewestPrerelease: b.NewestPrerelease}
	}
	return lines
}

// Check reports, together, every way the snapshot and the catalog
// disagree: an endoflife, actions-manifest, adoptium, maven-metadata,
// gradle-services or rust-channel source of the catalog with no entry,
// an entry for a source the catalog no longer names, and a line or
// release that is not valid under the version scheme of a tool using
// the source.
func (s *Snapshot) Check(cat *catalog.Catalog) error {
	products := make(map[string][]string)
	repos := make(map[string][]string)
	artifacts := make(map[string][]string)
	var adoptiumSchemes, gradleSchemes, rustSchemes []string
	for _, id := range cat.IDs() {
		tool := cat.Tools[id]
		for _, src := range tool.Sources {
			switch src.Type {
			case TypeEndOfLife:
				products[src.Product] = append(products[src.Product], tool.VersionScheme)
			case TypeActionsManifest:
				repos[src.Repository] = append(repos[src.Repository], tool.VersionScheme)
			case TypeAdoptium:
				adoptiumSchemes = append(adoptiumSchemes, tool.VersionScheme)
			case TypeGradleServices:
				gradleSchemes = append(gradleSchemes, tool.VersionScheme)
			case TypeRustChannel:
				rustSchemes = append(rustSchemes, tool.VersionScheme)
			case TypeMavenMetadata:
				artifacts[src.Artifact] = append(artifacts[src.Artifact], tool.VersionScheme)
			}
		}
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(products)) {
		p, ok := s.EndOfLife[name]
		if !ok {
			errs = append(errs, fmt.Errorf("snapshot: endoflife product %q is missing", name))
			continue
		}
		for _, f := range p.Releases {
			errs = appendInvalidLine(errs, "endoflife product "+name, f.Line, products[name])
		}
	}
	errs = checkManifests(errs, "actions-manifest repository", repos, s.ActionsManifest)
	errs = checkManifests(errs, "maven-metadata artifact", artifacts, s.MavenMetadata)
	var adoptiumLines, gradleLines []string
	if s.Adoptium != nil {
		adoptiumLines = s.Adoptium.Lines
	}
	if s.GradleServices != nil {
		gradleLines = slices.SortedFunc(maps.Keys(s.GradleServices.Lines), release.CompareLines)
	}
	errs = checkEntry(errs, "adoptium", "adoptium releases", adoptiumSchemes, s.Adoptium != nil, adoptiumLines)
	errs = checkEntry(errs, "gradle-services", "gradle-services versions", gradleSchemes, s.GradleServices != nil, gradleLines)
	errs = s.checkRustChannel(errs, rustSchemes)
	for _, name := range slices.Sorted(maps.Keys(s.EndOfLife)) {
		if _, ok := products[name]; !ok {
			errs = append(errs, fmt.Errorf("snapshot: endoflife product %q is not in the catalog", name))
		}
	}
	return errors.Join(errs...)
}

// checkEntry appends an error when the catalog names a source, kind,
// that has schemes and the snapshot has no entry for it, or the
// reverse, and an error for each line of the entry that is not valid
// under those schemes. what names the entry, such as "adoptium
// releases".
func checkEntry(errs []error, kind, what string, schemes []string, present bool, lines []string) []error {
	switch {
	case schemes != nil && !present:
		return append(errs, fmt.Errorf("snapshot: %s are missing", what))
	case schemes == nil && present:
		return append(errs, fmt.Errorf("snapshot: %s are not in the catalog", what))
	}
	for _, line := range lines {
		errs = appendInvalidLine(errs, kind, line, schemes)
	}
	return errs
}

// checkRustChannel appends an error when the catalog names a
// rust-channel source and the snapshot has no entry for it, or the
// reverse, and an error when the entry's release is not a stable
// release under the schemes of the tools using it.
func (s *Snapshot) checkRustChannel(errs []error, schemes []string) []error {
	switch {
	case schemes != nil && s.RustChannel == nil:
		return append(errs, errors.New("snapshot: rust-channel release is missing"))
	case schemes == nil && s.RustChannel != nil:
		return append(errs, errors.New("snapshot: rust-channel release is not in the catalog"))
	case s.RustChannel == nil:
		return errs
	}
	for _, scheme := range slices.Compact(slices.Sorted(slices.Values(schemes))) {
		if v, err := release.Parse(scheme, s.RustChannel.Version); err != nil || v.Prerelease {
			errs = append(errs, fmt.Errorf("snapshot: rust-channel: release %q is not a stable release under version scheme %q", s.RustChannel.Version, scheme))
		}
	}
	return errs
}

// checkManifests appends an error for each name of want that have
// lacks, each line of an entry that is not valid under the schemes
// want gives it, and each entry of have that want does not name. kind
// describes a name, such as "actions-manifest repository".
func checkManifests(errs []error, kind string, want map[string][]string, have map[string]Manifest) []error {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		m, ok := have[name]
		if !ok {
			errs = append(errs, fmt.Errorf("snapshot: %s %q is missing", kind, name))
			continue
		}
		for _, line := range slices.SortedFunc(maps.Keys(m.Lines), release.CompareLines) {
			errs = appendInvalidLine(errs, kind+" "+name, line, want[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(have)) {
		if _, ok := want[name]; !ok {
			errs = append(errs, fmt.Errorf("snapshot: %s %q is not in the catalog", kind, name))
		}
	}
	return errs
}

// appendInvalidLine appends an error when line is not valid under
// every scheme given.
func appendInvalidLine(errs []error, where, line string, schemes []string) []error {
	for _, scheme := range slices.Compact(slices.Sorted(slices.Values(schemes))) {
		if !release.ValidLine(line, scheme) {
			errs = append(errs, fmt.Errorf("snapshot: %s: line %q is not valid under version scheme %q", where, line, scheme))
		}
	}
	return errs
}
