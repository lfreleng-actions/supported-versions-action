// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source/actionsmanifest"
)

// facts is what the sources of one tool supplied.
type facts struct {
	// facts are the lifecycle facts of every endoflife source.
	facts []release.Facts
	// availability merges every actions-manifest source; nil when the
	// tool has none or none could be supplied.
	availability map[string]policy.Build
	// sources holds one entry per catalog source, in catalog order.
	sources []SourceEntry
	// warnings are the sources' warnings, in catalog order.
	warnings []string
	// snapshot reports that any source used the snapshot.
	snapshot bool
}

// fetched is what one source supplied.
type fetched struct {
	entry        SourceEntry
	facts        []release.Facts
	availability map[string]policy.Build
	warnings     []string
	snapshot     bool
	err          error
}

// gather fetches the facts of every source of tool concurrently. When
// line is not "", endoflife sources fetch that one line rather than
// the whole list, and the availability is cut to it.
func (r *Resolver) gather(ctx context.Context, tool catalog.Tool, line string) (facts, error) {
	results := make([]fetched, len(tool.Sources))
	var wg sync.WaitGroup
	for i, src := range tool.Sources {
		wg.Go(func() {
			results[i] = r.fetch(ctx, tool.VersionScheme, src, line)
		})
	}
	wg.Wait()

	var out facts
	for _, res := range results {
		if res.err != nil {
			return facts{}, res.err
		}
		out.sources = append(out.sources, res.entry)
		out.facts = append(out.facts, res.facts...)
		out.warnings = append(out.warnings, res.warnings...)
		out.snapshot = out.snapshot || res.snapshot
		if res.availability != nil {
			if out.availability == nil {
				out.availability = make(map[string]policy.Build)
			}
			for l, b := range res.availability {
				out.availability[l] = b
			}
		}
	}
	return out, nil
}

// fetch reads one source.
func (r *Resolver) fetch(ctx context.Context, scheme string, src catalog.Source, line string) fetched {
	switch src.Type {
	case snapshot.TypeEndOfLife:
		return r.fetchEndOfLife(ctx, src.Product, line)
	case snapshot.TypeActionsManifest:
		return r.fetchManifest(ctx, scheme, src.Repository, line)
	default:
		return fetched{err: fmt.Errorf("%w %q", ErrUnsupportedSource, src.Type)}
	}
}

func (r *Resolver) fetchEndOfLife(ctx context.Context, product, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeEndOfLife, Ref: product}}
	var (
		res source.Result
		err error
	)
	if line != "" {
		var f release.Facts
		f, res, err = r.eol.Release(ctx, product, line)
		switch {
		case err == nil:
			out.facts = []release.Facts{f}
		case errors.Is(err, source.ErrNotFound):
			// The product answered, without the line: its status is
			// unknown, which is no error.
			out.entry.Kind = KindLive
			out.entry.FetchedAt = timePtr(stamp(r.now()))
			return out
		}
	} else {
		out.facts, res, err = r.eol.Releases(ctx, product)
	}
	switch {
	case err == nil:
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
		return out
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	}
	all, fetchedAt, found := r.snapshot.Facts(product)
	if !found {
		out.err = fmt.Errorf("%w for endoflife product %q: %w", ErrNoFacts, product, err)
		return out
	}
	out.facts = all
	if line != "" {
		out.facts = nil
		for _, f := range all {
			if release.CompareLines(f.Line, line) == 0 {
				out.facts = []release.Facts{f}
			}
		}
	}
	out.entry.Kind = KindSnapshot
	out.entry.FetchedAt = timePtr(stamp(fetchedAt))
	out.snapshot = true
	return out
}

func (r *Resolver) fetchManifest(ctx context.Context, scheme, repo, line string) fetched {
	out := fetched{entry: SourceEntry{Type: snapshot.TypeActionsManifest, Ref: repo}}
	var lines map[string]actionsmanifest.Line
	builds, res, err := r.manifests.Builds(ctx, repo)
	switch {
	case err == nil:
		lines = actionsmanifest.Lines(builds, scheme)
		out.entry.Kind = Kind(res.Source)
		out.entry.FetchedAt = timePtr(stamp(res.FetchedAt))
		out.warnings = res.Warnings
	case !errors.Is(err, source.ErrNoData):
		out.err = err
		return out
	default:
		var (
			fetchedAt time.Time
			found     bool
		)
		lines, fetchedAt, found = r.snapshot.Lines(repo)
		if !found {
			// The policy warns that it has no installability source
			// and admits released lines.
			out.entry.Kind = KindUnavailable
			return out
		}
		out.entry.Kind = KindSnapshot
		out.entry.FetchedAt = timePtr(stamp(fetchedAt))
		out.snapshot = true
	}
	out.availability = make(map[string]policy.Build, len(lines))
	for l, b := range lines {
		if line != "" && release.CompareLines(l, line) != 0 {
			continue
		}
		out.availability[l] = policy.Build{Stable: b.NewestStable, Prerelease: b.NewestPrerelease}
	}
	return out
}

func timePtr(t time.Time) *time.Time {
	return &t
}
