// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

func ptr[T any](v T) *T { return &v }

func TestOverrideWinsOverOverlay(t *testing.T) {
	u := newUpstream(t)
	const origin = "overlay.yaml; flags"
	r := newResolver(t, u, func(o *versions.Options) {
		o.Overlay = overlay(t, "schema: 1\ntools:\n  python:\n    policy:\n      floor: \"3.9\"\n      prerelease: true\n      eol_behaviour: fail\n") //nolint:misspell // field name fixed by the catalog format
		o.Override = &catalog.PolicyOverride{Prerelease: ptr(false), EOLBehaviour: ptr(catalog.EOLStrip)}
		o.OverlayOrigin = origin
	})
	rep, err := r.Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatalf("err = %v, want the override's strip to replace the overlay's fail", err)
	}
	validated(t, rep)
	p := rep.Policy
	if p.Prerelease || p.EOLBehaviour != catalog.EOLStrip || p.Floor != "3.9" || !p.Overlay || p.Origin != origin {
		t.Errorf("policy = %+v, want the override over the overlay", p)
	}
	for _, l := range []string{"3.9", "3.10"} {
		if got := lineOf(t, rep, l); got.InMatrix || got.Reason != policy.ReasonEOLStripped {
			t.Errorf("%s = %+v, want stripped", l, got)
		}
	}
}

func TestOverrideEveryTool(t *testing.T) {
	u := newUpstream(t)
	r := newResolver(t, u, func(o *versions.Options) {
		o.Override = &catalog.PolicyOverride{EOLBehaviour: ptr(catalog.EOLAllow)}
		o.OverlayOrigin = "flags"
	})
	// "golang" is an alias of the go tool, which the override reaches
	// through its id.
	rep, err := r.Matrix(context.Background(), "golang", versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	if rep.Tool != "go" || rep.Policy.EOLBehaviour != catalog.EOLAllow || !rep.Policy.Overlay || rep.Policy.Origin != "flags" {
		t.Errorf("policy = %+v, want the override applied to go", rep.Policy)
	}
}

func TestOverrideInvalid(t *testing.T) {
	u := newUpstream(t)
	opts := versions.Options{
		Snapshot:         embeddedSnapshot(t),
		EndOfLifeBaseURL: u.srv.URL + eolPrefix,
		ManifestBaseURL:  u.srv.URL + rawPrefix,
		Override:         &catalog.PolicyOverride{EOLBehaviour: ptr("bogus")},
	}
	if _, err := versions.New(opts); err == nil {
		t.Error("New with an invalid override: want an error")
	}
}
