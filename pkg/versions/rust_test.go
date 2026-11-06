// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package versions_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/policy"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/versions"
)

const (
	rust        = "rust"
	rustPath    = rustPrefix + "dist/channel-rust-stable.toml"
	rustLatest  = "1.99"
	rustNewest  = "1.99.1"
	rustStable  = "stable"
	rustCargo   = "Cargo.toml"
	rustChannel = "1.99.2"
)

// rustMatrix is the six newest lines on the fixed clock, then the
// stable label.
var rustMatrix = strings.Fields("1.94 1.95 1.96 1.97 1.98 1.99 stable")

func rustReport(t *testing.T, u *upstream, edit func(*versions.Options), req versions.MatrixRequest) versions.Report {
	t.Helper()
	rep, err := newResolver(t, u, edit).Matrix(context.Background(), rust, req)
	if err != nil {
		t.Fatal(err)
	}
	validated(t, rep)
	return rep
}

func TestMatrixRustLive(t *testing.T) {
	u := newUpstream(t)
	rep := rustReport(t, u, nil, versions.MatrixRequest{})
	if rep.Tool != rust || rep.Matrix.Key != "rust-version" || rep.Latest != rustLatest {
		t.Errorf("tool %q, key %q, latest %q", rep.Tool, rep.Matrix.Key, rep.Latest)
	}
	wantVersions(t, rep, rustMatrix...)
	if rep.Source.Kind == versions.KindSnapshot || len(rep.Source.Sources) != 2 ||
		rep.Source.Sources[1].Type != snapshot.TypeRustChannel {
		t.Errorf("source = %+v", rep.Source)
	}
	if len(rep.Warnings) != 0 {
		t.Errorf("warnings = %v, want none for end-of-life lines Rust allows", rep.Warnings)
	}
	if l := lineOf(t, rep, "1.98"); !l.InMatrix || l.Status != release.StatusEOL {
		t.Errorf("1.98 = %+v, want end of life and in the matrix", l)
	}
	if l := lineOf(t, rep, rustLatest); !l.InMatrix || l.Newest != rustNewest || !l.Installable {
		t.Errorf("%s = %+v, want installable at %s", rustLatest, l, rustNewest)
	}
	if u.count(rustPath) != 1 {
		t.Errorf("channel requests = %d, want 1", u.count(rustPath))
	}
}

// TestMatrixRustChannelAhead checks that the channel decides what is
// installable: a point release raises its line's newest release, and a
// line endoflife.date does not list yet is reported installable but
// waits for its lifecycle facts to enter the matrix, which the stable
// label covers meanwhile.
func TestMatrixRustChannelAhead(t *testing.T) {
	day := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		channel, line, reason string
	}{
		"point release": {rustChannel, rustLatest, ""},
		"new line":      {"1.100.0", "1.100", policy.ReasonNoFacts},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUpstream(t)
			u.bodies[rustPath] = rustBody(tc.channel, day)
			rep := rustReport(t, u, nil, versions.MatrixRequest{})
			wantVersions(t, rep, rustMatrix...)
			if rep.Latest != rustLatest {
				t.Errorf("latest = %q, want %q", rep.Latest, rustLatest)
			}
			if l := lineOf(t, rep, tc.line); l.Newest != tc.channel || !l.Installable || l.Reason != tc.reason {
				t.Errorf("%s = %+v, want installable at %s", tc.line, l, tc.channel)
			}
		})
	}
}

func TestMatrixRustChannelFallback(t *testing.T) {
	u := newUpstream(t)
	u.setStatus(rustPath, http.StatusInternalServerError)
	rep := rustReport(t, u, nil, versions.MatrixRequest{})
	wantVersions(t, rep, rustMatrix...)
	if s := rep.Source.Sources[1]; s.Kind != versions.KindSnapshot || s.FetchedAt == nil {
		t.Errorf("rust-channel source = %+v, want snapshot", s)
	}
}

func TestMatrixRustDetected(t *testing.T) {
	for name, tc := range map[string]struct {
		file, text, origin string
		want               []string
	}{
		"cargo": {
			rustCargo, "[package]\nname = \"demo\"\nrust-version = \"1.97\"\n",
			rustCargo + ":package.rust-version", strings.Fields("1.97 1.98 1.99 stable"),
		},
		"toolchain channel": {
			"rust-toolchain.toml", "[toolchain]\nchannel = \"stable\"\n",
			"", rustMatrix,
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := nodeProject(t, map[string]string{tc.file: tc.text})
			rep := rustReport(t, newUpstream(t), nil, versions.MatrixRequest{ProjectDir: dir})
			wantVersions(t, rep, tc.want...)
			if tc.origin == "" {
				if rep.Constraint != nil && rep.Constraint.Outcome == constraint.OutcomeApplied {
					t.Errorf("constraint = %+v, want none applied for a channel name", rep.Constraint)
				}
				if !hasWarning(rep, `ignoring "stable"`) {
					t.Errorf("warnings = %v, want the channel name ignored", rep.Warnings)
				}
				return
			}
			if rep.Constraint == nil || rep.Constraint.Origin != tc.origin ||
				rep.Constraint.Outcome != constraint.OutcomeApplied {
				t.Errorf("constraint = %+v", rep.Constraint)
			}
		})
	}
}

// TestOverrideKeepsRustEOL checks that an end-of-life override leaves
// Rust's allow in place while still reaching other tools, and that an
// overlay setting Rust's EOLBehaviour lets the override through.
func TestOverrideKeepsRustEOL(t *testing.T) {
	override := func(o *versions.Options) {
		o.Override = &catalog.PolicyOverride{EOLBehaviour: ptr(catalog.EOLFail)}
	}
	u := newUpstream(t)
	rep := rustReport(t, u, override, versions.MatrixRequest{})
	wantVersions(t, rep, rustMatrix...)
	if rep.Policy.EOLBehaviour != catalog.EOLAllow {
		t.Errorf("rust EOLBehaviour = %q, want allow", rep.Policy.EOLBehaviour)
	}
	py, err := newResolver(t, u, override).Matrix(context.Background(), python, versions.MatrixRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if py.Policy.EOLBehaviour != catalog.EOLFail {
		t.Errorf("python EOLBehaviour = %q, want the override's fail", py.Policy.EOLBehaviour)
	}

	r := newResolver(t, u, func(o *versions.Options) {
		override(o)
		o.Overlay = overlay(t, "schema: 1\ntools:\n  rust:\n    policy:\n      eol_behaviour: warn\n") //nolint:misspell // field name fixed by the catalog format
	})
	if _, err := r.Matrix(context.Background(), rust, versions.MatrixRequest{}); err == nil {
		t.Error("rust with an overlay EOLBehaviour and a fail override: want an end-of-life error")
	}
}
