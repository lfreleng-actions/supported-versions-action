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
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/endoflife"
)

// Generate fetches every endoflife and actions-manifest source of the
// catalog's tools, in cat.IDs() order, and returns their snapshot.
// Manifests are reduced to lines under the version scheme of the first
// tool naming them. A source two tools share is fetched once. Other
// source types are skipped. Any fetch error fails the whole generation,
// so a partial snapshot is never returned. GeneratedAt is now(), or
// time.Now when now is nil.
func Generate(ctx context.Context, cat *catalog.Catalog, eol *endoflife.Client, manifests *actionsmanifest.Client, now func() time.Time) (*Snapshot, error) {
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
				s.EndOfLife[src.Product] = Product{FetchedAt: stamp(res.FetchedAt), Releases: facts}
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
			}
		}
	}
	s.GeneratedAt = stamp(now())
	return s, nil
}

// Check reports, together, every way the snapshot and the catalog
// disagree: an endoflife or actions-manifest source of the catalog
// with no entry, an entry for a source the catalog no longer names,
// and a line that is not valid under the version scheme of a tool
// using the source.
func (s *Snapshot) Check(cat *catalog.Catalog) error {
	products := make(map[string][]string)
	repos := make(map[string][]string)
	for _, id := range cat.IDs() {
		tool := cat.Tools[id]
		for _, src := range tool.Sources {
			switch src.Type {
			case TypeEndOfLife:
				products[src.Product] = append(products[src.Product], tool.VersionScheme)
			case TypeActionsManifest:
				repos[src.Repository] = append(repos[src.Repository], tool.VersionScheme)
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
	for _, repo := range slices.Sorted(maps.Keys(repos)) {
		m, ok := s.ActionsManifest[repo]
		if !ok {
			errs = append(errs, fmt.Errorf("snapshot: actions-manifest repository %q is missing", repo))
			continue
		}
		for _, line := range slices.SortedFunc(maps.Keys(m.Lines), release.CompareLines) {
			errs = appendInvalidLine(errs, "actions-manifest repository "+repo, line, repos[repo])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.EndOfLife)) {
		if _, ok := products[name]; !ok {
			errs = append(errs, fmt.Errorf("snapshot: endoflife product %q is not in the catalog", name))
		}
	}
	for _, repo := range slices.Sorted(maps.Keys(s.ActionsManifest)) {
		if _, ok := repos[repo]; !ok {
			errs = append(errs, fmt.Errorf("snapshot: actions-manifest repository %q is not in the catalog", repo))
		}
	}
	return errors.Join(errs...)
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
