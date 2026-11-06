// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/detect"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

const (
	fileCargo     = "Cargo.toml"
	fileToolchain = "rust-toolchain.toml"
	originCargo   = fileCargo + ":package.rust-version"
	originChannel = fileToolchain + ":toolchain.channel"
)

var rustLines = strings.Fields("1.90 1.91 1.92 1.93 1.94 1.95")

// rustTool reads the rules the catalog's Rust entry gives: Cargo's
// rust-version, then the rust-toolchain.toml channel.
func rustTool() catalog.Tool {
	return catalog.Tool{
		VersionScheme: release.SchemeMajorMinor,
		Constraints: &catalog.Constraints{
			Syntax: constraint.SyntaxSemverMin,
			Detect: []catalog.DetectRule{
				{File: fileCargo, Field: "package.rust-version"},
				{File: fileToolchain, Field: "toolchain.channel"},
			},
		},
	}
}

func TestRustDetected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		files  map[string]string
		origin string
		want   string
	}{
		{"rust-version", map[string]string{fileCargo: "[package]\nname = \"x\"\nrust-version = \"1.92\"\n"},
			originCargo, "1.92 1.93 1.94 1.95"},
		{"rust-version with patch", map[string]string{fileCargo: "[package]\nrust-version = \"1.93.1\"\n"},
			originCargo, "1.93 1.94 1.95"},
		{"workspace inheritance", map[string]string{fileCargo: "[workspace]\nmembers = [\"a\"]\n" +
			"[workspace.package]\nrust-version = \"1.94\"\n[package]\nname = \"x\"\nrust-version.workspace = true\n"},
			originCargo, "1.94 1.95"},
		{"Cargo first", map[string]string{fileCargo: "[package]\nrust-version = \"1.91\"\n",
			fileToolchain: "[toolchain]\nchannel = \"1.95.0\"\n"},
			originCargo, "1.91 1.92 1.93 1.94 1.95"},
		{"toolchain channel", map[string]string{fileToolchain: "[toolchain]\nchannel = \"1.93.0\"\ncomponents = [\"rustfmt\"]\n"},
			originChannel, "1.93 1.94 1.95"},
		{"no rust-version", map[string]string{fileCargo: "[package]\nname = \"x\"\n",
			fileToolchain: "[toolchain]\nchannel = \"1.94\"\n"},
			originChannel, "1.94 1.95"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, tc.files), rustTool())
			if !d.Found || d.Constraint == nil || d.Origin != tc.origin {
				t.Fatalf("detection = %+v", d)
			}
			if got := allowed(d, rustLines); !slices.Equal(got, strings.Fields(tc.want)) {
				t.Errorf("allows %v, want %s (text %q)", got, tc.want, d.Text)
			}
			if len(d.Warnings) != 0 {
				t.Errorf("warnings = %q", d.Warnings)
			}
		})
	}
}

func TestRustChannelIgnored(t *testing.T) {
	for _, channel := range []string{"stable", "beta", "nightly", "nightly-2026-10-01", "stable-x86_64-unknown-linux-gnu"} {
		t.Run(channel, func(t *testing.T) {
			dir := project(t, map[string]string{fileToolchain: "[toolchain]\nchannel = \"" + channel + "\"\n"})
			d := detectOK(t, dir, rustTool())
			if d.Found || d.Constraint != nil {
				t.Errorf("found %+v", d)
			}
			want := originChannel + ": ignoring \"" + channel + "\": a release channel, not a version"
			if !slices.Equal(d.Warnings, []string{want}) {
				t.Errorf("warnings = %q, want %q", d.Warnings, want)
			}
		})
	}
}

// TestRustWorkspaceRootElsewhere checks that a member inheriting
// rust-version from a workspace root in another directory yields
// nothing, with a warning, and later rules are tried.
func TestRustWorkspaceRootElsewhere(t *testing.T) {
	dir := project(t, map[string]string{
		fileCargo:     "[package]\nname = \"x\"\nrust-version = { workspace = true }\n",
		fileToolchain: "[toolchain]\nchannel = \"1.95\"\n",
	})
	d := detectOK(t, dir, rustTool())
	if !d.Found || d.Origin != originChannel {
		t.Errorf("detection = %+v, want the toolchain channel", d)
	}
	want := originCargo + ": inherited from a workspace root Cargo.toml does not define; trying later rules"
	if !slices.Equal(d.Warnings, []string{want}) {
		t.Errorf("warnings = %q, want %q", d.Warnings, want)
	}
}

func TestRustInvalid(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"operator":        {fileCargo: "[package]\nrust-version = \">=1.74\"\n"},
		"prerelease":      {fileCargo: "[package]\nrust-version = \"1.74.0-beta.1\"\n"},
		"workspace value": {fileCargo: "[workspace.package]\nrust-version = 1.74\n[package]\nrust-version.workspace = true\n"},
		"bad table":       {fileCargo: "[package]\nrust-version = { workspace = false }\n"},
		"toolchain":       {fileToolchain: "[toolchain]\nchannel = \"1.74-beta\"\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if d, err := detect.Detect(project(t, files), rustTool()); err == nil {
				t.Errorf("Detect = %+v, want an error", d)
			}
		})
	}
}
