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
)

// Generate fetches every endoflife, actions-manifest, adoptium,
// maven-metadata and gradle-services source of the catalog's tools, in
// cat.IDs() order, and returns their snapshot. Manifests, Maven
// metadata and Gradle versions are reduced to lines under the version
// scheme of the first tool naming them, and endoflife products keep
// only the lines valid under it. A source two tools share is fetched once. Other
// source types are skipped. Any fetch error fails the whole generation,
// so a partial snapshot is never returned. GeneratedAt is now(), or
// time.Now when now is nil.
func Generate(ctx context.Context, cat *catalog.Catalog, eol *endoflife.Client, manifests *actionsmanifest.Client, releases *adoptium.Client, maven *mavenmeta.Client, gradle *gradleservices.Client, now func() time.Time) (*Snapshot, error) {
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
// disagree: an endoflife, actions-manifest, adoptium, maven-metadata or
// gradle-services source of the catalog with no entry, an entry for a source the catalog no longer names,
// and a line that is not valid under the version scheme of a tool
// using the source.
func (s *Snapshot) Check(cat *catalog.Catalog) error {
	products := make(map[string][]string)
	repos := make(map[string][]string)
	artifacts := make(map[string][]string)
	var adoptiumSchemes, gradleSchemes []string
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
	switch {
	case adoptiumSchemes != nil && s.Adoptium == nil:
		errs = append(errs, errors.New("snapshot: adoptium releases are missing"))
	case adoptiumSchemes == nil && s.Adoptium != nil:
		errs = append(errs, errors.New("snapshot: adoptium releases are not in the catalog"))
	case s.Adoptium != nil:
		for _, line := range s.Adoptium.Lines {
			errs = appendInvalidLine(errs, "adoptium", line, adoptiumSchemes)
		}
	}
	switch {
	case gradleSchemes != nil && s.GradleServices == nil:
		errs = append(errs, errors.New("snapshot: gradle-services versions are missing"))
	case gradleSchemes == nil && s.GradleServices != nil:
		errs = append(errs, errors.New("snapshot: gradle-services versions are not in the catalog"))
	case s.GradleServices != nil:
		for _, line := range slices.SortedFunc(maps.Keys(s.GradleServices.Lines), release.CompareLines) {
			errs = appendInvalidLine(errs, "gradle-services", line, gradleSchemes)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.EndOfLife)) {
		if _, ok := products[name]; !ok {
			errs = append(errs, fmt.Errorf("snapshot: endoflife product %q is not in the catalog", name))
		}
	}
	return errors.Join(errs...)
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
