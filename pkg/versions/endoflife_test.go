// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

// offScheme is a line endoflife.date may list for a major.minor tool
// from before the tool adopted the scheme, as it does Maven's 1 and 2.
const offScheme = "1"

// withOffSchemeLine returns the embedded snapshot's go facts with an
// off-scheme line added.
func withOffSchemeLine(t *testing.T) []release.Facts {
	t.Helper()
	facts, _, ok := embeddedSnapshot(t).Facts("go")
	if !ok {
		t.Fatal("no go facts in the embedded snapshot")
	}
	return append(facts, release.Facts{Line: offScheme, IsEOL: new(true)})
}

func checkNoOffSchemeLine(t *testing.T, r *versions.Resolver) {
	t.Helper()
	rep, err := r.Matrix(context.Background(), "go", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	for _, l := range rep.Lines {
		if l.Line == offScheme {
			t.Errorf("off-scheme line reported: %+v", l)
		}
	}
	if len(rep.Matrix.Versions) == 0 {
		t.Error("empty matrix")
	}
}

func TestEndOfLifeSkipsOffSchemeLinesLive(t *testing.T) {
	u := newUpstream(t)
	releases := make([]apiRelease, 0)
	for _, f := range withOffSchemeLine(t) {
		releases = append(releases, toAPI(f))
	}
	u.bodies[goList] = envelope(t, map[string]any{"name": "go", "releases": releases})
	checkNoOffSchemeLine(t, newResolver(t, u, nil))
}

func TestEndOfLifeSkipsOffSchemeLinesSnapshot(t *testing.T) {
	u := newUpstream(t)
	u.setStatus(goList, http.StatusServiceUnavailable)
	r := newResolver(t, u, func(o *versions.Options) {
		p := o.Snapshot.EndOfLife["go"]
		p.Releases = withOffSchemeLine(t)
		o.Snapshot.EndOfLife["go"] = p
	})
	checkNoOffSchemeLine(t, r)
}
