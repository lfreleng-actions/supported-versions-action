// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/detect"
)

const (
	filePackageJSON = "package.json"
	fileNvmrc       = ".nvmrc"
	fileNodeVersion = ".node-version"
	originEngines   = "package.json:engines.node"
	parseErr        = "parse package.json"
	nvmLTS          = "lts/*\n"
)

var nodeLines = []string{"18", "20", "22", "24", "26"}

// nodeTool is a tool with the detect rules of the catalog's nodejs.
func nodeTool() catalog.Tool {
	return catalog.Tool{
		Name:          "Node.js",
		VersionScheme: "major",
		Constraints: &catalog.Constraints{Syntax: "semver-range", Detect: []catalog.DetectRule{
			{File: filePackageJSON, Field: "engines.node"},
			{File: fileNvmrc},
			{File: fileNodeVersion},
		}},
	}
}

func expectNode(t *testing.T, d detect.Detection, origin string, want []string) {
	t.Helper()
	if !d.Found || d.Constraint == nil {
		t.Fatalf("not found: %+v", d)
	}
	if d.Origin != origin {
		t.Errorf("origin = %q, want %q", d.Origin, origin)
	}
	if got := allowed(d, nodeLines); !slices.Equal(got, want) {
		t.Errorf("allows %v, want %v (text %q)", got, want, d.Text)
	}
}

func TestPackageJSONEngines(t *testing.T) {
	node := nodeTool()
	for _, tc := range []struct {
		name, json string
		want       []string
	}{
		{"caret union", `{"engines": {"node": "^20.19.0 || ^22.13.0 || >=24"}}`, []string{"20", "22", "24", "26"}},
		{"floor", `{"name": "x", "engines": {"node": ">=18.18.0"}}`, nodeLines},
		{"range", `{"engines": {"npm": ">=10", "node": ">=20 <25"}}`, []string{"20", "22", "24"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{filePackageJSON: tc.json}), node)
			expectNode(t, d, originEngines, tc.want)
		})
	}
}

// TestPackageJSONFallsThrough checks that a package.json without
// engines.node, or with an empty one, yields to the version files.
func TestPackageJSONFallsThrough(t *testing.T) {
	node := nodeTool()
	for name, pkg := range map[string]string{
		"no engines": `{"name": "x"}`,
		"no node":    `{"engines": {"npm": ">=10"}}`,
		"null":       `{"engines": {"node": null}}`,
		"empty":      `{"engines": {"node": " "}}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{filePackageJSON: pkg, fileNvmrc: "v22.11.0\n"}), node)
			expectNode(t, d, fileNvmrc, []string{"22"})
		})
	}
}

func TestPackageJSONErrors(t *testing.T) {
	node := nodeTool()
	for name, tc := range map[string]struct{ json, want string }{
		"invalid":  {`{"engines": `, parseErr},
		"trailing": {`{} {}`, parseErr},
		"brace":    {`{"engines":{"node":">=24"}}}`, parseErr},
		"garbage":  {`{"engines":{"node":">=24"}} x`, parseErr},
		"number":   {`{"engines": {"node": 22}}`, originEngines},
		"engines":  {`{"engines": "node"}`, ""},
		"bad text": {`{"engines": {"node": "latest"}}`, originEngines},
		"comma":    {`{"engines": {"node": ","}}`, originEngines},
		"commas":   {`{"engines": {"node": " , ,"}}`, originEngines},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := detect.Detect(project(t, map[string]string{filePackageJSON: tc.json}), node)
			if tc.want == "" {
				// A path through a non-object finds nothing.
				if err != nil || d.Found {
					t.Errorf("found %v, err %v; want nothing", d.Found, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v; want one naming %s", err, tc.want)
			}
		})
	}
}

func TestNodeVersionFiles(t *testing.T) {
	node := nodeTool()
	for _, file := range []string{fileNvmrc, fileNodeVersion} {
		for _, content := range []string{"22\n", "v22.11.0\n", "22.11\n", "# pinned\nnode22\n"} {
			d := detectOK(t, project(t, map[string]string{file: content}), node)
			expectNode(t, d, file, []string{"22"})
			if d.Text != "==22" {
				t.Errorf("%s %q: text = %q", file, content, d.Text)
			}
		}
	}
}

func TestNvmAliases(t *testing.T) {
	node := nodeTool()
	for _, file := range []string{fileNvmrc, fileNodeVersion} {
		for content, want := range map[string]string{
			nvmLTS:      "lts/*",
			"lts/Jod\n": "lts/jod",
			"lts/-1\n":  "lts/-1",
			"node\n":    "node",
			"stable\n":  "stable",
		} {
			d := detectOK(t, project(t, map[string]string{file: content}), node)
			if d.Found || d.Constraint != nil || d.Alias != want || d.Text != want || d.Origin != file {
				t.Errorf("%s %q: %+v, want alias %q", file, content, d, want)
			}
		}
	}

	// A version earlier in the rule order wins over an alias.
	d := detectOK(t, project(t, map[string]string{
		filePackageJSON: `{"engines": {"node": ">=22"}}`,
		fileNvmrc:       nvmLTS,
	}), node)
	if d.Alias != "" {
		t.Errorf("alias %q, want package.json to win", d.Alias)
	}
	expectNode(t, d, originEngines, []string{"22", "24", "26"})

	// "system" and "iojs" name no release line: a warning, and the
	// next rule is tried.
	d = detectOK(t, project(t, map[string]string{fileNvmrc: "system\n", fileNodeVersion: "24\n"}), node)
	expectNode(t, d, fileNodeVersion, []string{"24"})
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "system") {
		t.Errorf("warnings = %q", d.Warnings)
	}
}

// TestAliasOnlyInNvmFiles checks that other version files do not read
// nvm aliases.
func TestAliasOnlyInNvmFiles(t *testing.T) {
	tl := nodeTool()
	tl.Constraints.Detect = []catalog.DetectRule{{File: ".tool-version"}}
	d := detectOK(t, project(t, map[string]string{".tool-version": nvmLTS}), tl)
	if d.Found || d.Alias != "" || len(d.Warnings) != 1 {
		t.Errorf("%+v, want a warning and nothing found", d)
	}
}
