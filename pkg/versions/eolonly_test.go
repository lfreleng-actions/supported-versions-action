// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

// TestMatrixEndOfLifeOnlyOffline checks each tool whose only source is
// endoflife.date against the embedded snapshot on the fixed clock: the
// matrix key and lines, and that the bare minimum a caller passes as
// the explicit constraint narrows the matrix to that line and newer.
func TestMatrixEndOfLifeOnlyOffline(t *testing.T) {
	for name, tc := range map[string]struct {
		id, key, matrix, minimum, narrowed string
	}{
		"dotnet":    {"dotnet", "dotnet-version", "8 9 10", "9.0", "9 10"},
		"terraform": {"terraform", "terraform-version", "1.16 1.17", "1.17.0", "1.17"},
		"ruby":      {"ruby", "ruby-version", "3.3 3.4 4.0", "3.4", "3.4 4.0"},
		"php":       {"php", "php-version", "8.2 8.3 8.4 8.5", "8.4", "8.4 8.5"},
		"elixir":    {"elixir", "elixir-version", "1.16 1.17 1.18 1.19 1.20", "1.18", "1.18 1.19 1.20"},
		"haskell":   {"ghc", "ghc-version", "9.6 9.8 9.10 9.12 9.14", "9.10.1", "9.10 9.12 9.14"},
		"julia":     {"julia", "julia-version", "1.10 1.13", "1.11", "1.13"},
		"scala":     {"scala", "scala-version", "2.10 2.11 2.12 2.13 3.3 3.9 3.10", "2.13", "2.13 3.3 3.9 3.10"},
		"k8s":       {"kubernetes", "kubernetes-version", "1.35 1.36 1.37", "1.36", "1.36 1.37"},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUpstream(t)
			r := newResolver(t, u, func(o *versions.Options) { o.Fetch.Offline = true })
			rep, err := r.Matrix(context.Background(), name, versions.MatrixRequest{})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			lines := strings.Fields(tc.matrix)
			if rep.Tool != tc.id || rep.Matrix.Key != tc.key || rep.Latest != lines[len(lines)-1] {
				t.Errorf("tool %q, key %q, latest %q", rep.Tool, rep.Matrix.Key, rep.Latest)
			}
			if rep.Source.Kind != versions.KindSnapshot || len(rep.Source.Sources) != 1 {
				t.Errorf("source = %+v, want the snapshot's endoflife facts alone", rep.Source)
			}
			wantVersions(t, rep, lines...)

			rep, err = r.Matrix(context.Background(), name, versions.MatrixRequest{Constraint: tc.minimum})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			wantVersions(t, rep, strings.Fields(tc.narrowed)...)
			if rep.Constraint == nil || rep.Constraint.Text != tc.minimum ||
				rep.Constraint.Outcome != constraint.OutcomeApplied {
				t.Errorf("constraint = %+v", rep.Constraint)
			}
			if u.total() != 0 {
				t.Errorf("offline resolver made %d requests", u.total())
			}
		})
	}
}
