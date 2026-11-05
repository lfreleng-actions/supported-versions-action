// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	packageJSON   = "package.json"
	nvmrc         = ".nvmrc"
	originEngines = packageJSON + ":engines.node"
)

func nodeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMatrixNodeLive(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), "node", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Tool != "nodejs" || rep.Matrix.Key != "node-version" {
		t.Errorf("tool %q, key %q", rep.Tool, rep.Matrix.Key)
	}
	wantVersions(t, rep, "22", "24", "26")
	// Odd lines are never LTS.
	if l := lineOf(t, rep, "25"); l.InMatrix {
		t.Errorf("25 = %+v, want out of the matrix", l)
	}
}

func TestMatrixNodeDetected(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	for name, tc := range map[string]struct {
		files  map[string]string
		want   []string
		origin string
	}{
		"engines":   {map[string]string{packageJSON: `{"engines": {"node": "^22.12.0 || >=24.0.0"}}`}, []string{"22", "24", "26"}, originEngines},
		"floor":     {map[string]string{packageJSON: `{"engines": {"node": ">=24"}}`}, []string{"24", "26"}, originEngines},
		"nvmrc":     {map[string]string{nvmrc: "v24.11.0\n"}, []string{"24"}, nvmrc},
		"lts":       {map[string]string{nvmrc: "lts/*\n"}, []string{"26"}, nvmrc},
		"lts back":  {map[string]string{".node-version": "lts/-1\n"}, []string{"24"}, ".node-version"},
		"pkg first": {map[string]string{packageJSON: `{"engines": {"node": "22.x"}}`, nvmrc: "lts/*\n"}, []string{"22"}, originEngines},
		"stable":    {map[string]string{nvmrc: "node\n"}, []string{"26"}, nvmrc},
	} {
		t.Run(name, func(t *testing.T) {
			rep, err := r.Matrix(context.Background(), "nodejs", versions.MatrixRequest{ProjectDir: nodeProject(t, tc.files)})
			if err != nil {
				t.Fatal(err)
			}
			validated(t, rep)
			wantVersions(t, rep, tc.want...)
			if rep.Constraint == nil || rep.Constraint.Origin != tc.origin ||
				rep.Constraint.Outcome != constraint.OutcomeApplied {
				t.Errorf("constraint = %+v", rep.Constraint)
			}
		})
	}
}

func TestMatrixNodeUnresolvedAlias(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), "nodejs",
		versions.MatrixRequest{ProjectDir: nodeProject(t, map[string]string{nvmrc: "lts/jod\n"})})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "22", "24", "26")
	if rep.Constraint != nil {
		t.Errorf("constraint = %+v, want none", rep.Constraint)
	}
	if !hasWarning(rep, nvmrc+`: alias "lts/jod" is unresolved`) {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestMatrixNodeExplicitConstraint(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, nil)
	rep, err := r.Matrix(context.Background(), "nodejs", versions.MatrixRequest{Constraint: ">=20 <25"})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	wantVersions(t, rep, "22", "24")
}
