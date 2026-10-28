// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/detect"
)

const (
	py311 = "3.11"
	py312 = "3.12"
	py313 = "3.13"
	py314 = "3.14"
	py315 = "3.15"
	go126 = "1.26"
	go127 = "1.27"

	filePyproject     = "pyproject.toml"
	fileSetupCfg      = "setup.cfg"
	fileSetupPy       = "setup.py"
	filePythonVersion = ".python-version"
	fileGoMod         = "go.mod"
	originGo          = "go.mod:go"
	originToolchain   = "go.mod:toolchain"
	toolchainVer      = "1.27.3"
)

var (
	pyLines = []string{py311, py312, py313, py314, py315}
	goLines = []string{go126, go127}
)

func tool(t *testing.T, id string) catalog.Tool {
	t.Helper()
	c, err := catalog.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	tl, ok := c.Tools[id]
	if !ok {
		t.Fatalf("catalog has no %s", id)
	}
	return tl
}

// project writes files, mapping a path to its content, into a new
// directory and returns it.
func project(t *testing.T, files map[string]string) string {
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

func detectOK(t *testing.T, dir string, tl catalog.Tool) detect.Detection {
	t.Helper()
	d, err := detect.Detect(dir, tl)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	return d
}

func allowed(d detect.Detection, lines []string) []string {
	var got []string
	for _, l := range lines {
		if d.Constraint.Allows(l) {
			got = append(got, l)
		}
	}
	return got
}

func expect(t *testing.T, d detect.Detection, origin string, want []string) {
	t.Helper()
	if !d.Found || d.Constraint == nil {
		t.Fatalf("not found: %+v", d)
	}
	if d.Origin != origin {
		t.Errorf("origin = %q, want %q", d.Origin, origin)
	}
	lines := pyLines
	if strings.HasPrefix(origin, fileGoMod+":") {
		lines = goLines
	}
	if got := allowed(d, lines); !slices.Equal(got, want) {
		t.Errorf("allows %v, want %v (text %q)", got, want, d.Text)
	}
}

const classifiers3 = `classifiers = [
  "Programming Language :: Python :: 3",
  "Programming Language :: Python :: 3 :: Only",
  "Programming Language :: Python :: 3.11",
  "Programming Language :: Python :: 3.12",
  "Programming Language :: Python :: 3.13",
  "Programming Language :: Python :: Implementation :: CPython",
]
`

const (
	originRequires    = "pyproject.toml:project.requires-python"
	originPoetry      = "pyproject.toml:tool.poetry.dependencies.python"
	originClassifiers = "pyproject.toml:project.classifiers"
	originSetupCfg    = "setup.cfg:options.python_requires"
)

// TestPythonFixtures reproduces the fixtures of
// python-supported-versions-action.
func TestPythonFixtures(t *testing.T) {
	py := tool(t, "python")
	for _, tc := range []struct {
		name, pyproject, origin string
		want                    []string
	}{
		{"requires >=", "[project]\nrequires-python = \">=3.11\"\n", originRequires, pyLines},
		{"requires ==", "[project]\nrequires-python = \"==3.11\"\n", originRequires, []string{py311}},
		{"requires >", "[project]\nrequires-python = \">3.11\"\n", originRequires, pyLines[1:]},
		{"requires >= later", "[project]\nrequires-python = \">=3.12\"\n", originRequires, pyLines[1:]},
		{"requires range", "[project]\nrequires-python = \">=3.11,<3.15\"\n", originRequires, pyLines[:4]},
		{"requires range inclusive", "[project]\nrequires-python = \">=3.11,<=3.13\"\n", originRequires, pyLines[:3]},
		{"poetry caret", "[tool.poetry.dependencies]\npython = \"^3.11\"\n", originPoetry, pyLines},
		{"poetry ==", "[tool.poetry.dependencies]\npython = \"==3.12\"\n", originPoetry, []string{py312}},
		{"poetry ~= patch", "[tool.poetry.dependencies]\npython = \"~=3.11.0\"\n", originPoetry, []string{py311}},
		{"classifiers only", "[project]\nname = \"x\"\n" + classifiers3, originClassifiers, pyLines[:3]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{filePyproject: tc.pyproject}), py)
			expect(t, d, tc.origin, tc.want)
		})
	}
}

// TestPoetryCompatibleRelease is a deliberate difference from
// python-supported-versions-action, whose fixture expects Poetry's
// "~=X.Y" to allow line X.Y alone. PEP 440 makes it ">=X.Y, ==X.*",
// as build-metadata-action does, so later lines are allowed too.
func TestPoetryCompatibleRelease(t *testing.T) {
	d := detectOK(t, project(t, map[string]string{
		filePyproject: "[tool.poetry.dependencies]\npython = \"~=3.11\"\n",
	}), tool(t, "python"))
	expect(t, d, originPoetry, pyLines)
}

func TestNotFound(t *testing.T) {
	py := tool(t, "python")
	for name, files := range map[string]map[string]string{
		"no python information": {filePyproject: "[project]\nname = \"x\"\ndependencies = [\"requests\"]\n"},
		"empty project":         {},
		"empty requires":        {filePyproject: "[project]\nrequires-python = \"  \"\n"},
		"comma-only requires":   {filePyproject: "[project]\nrequires-python = \" , ,\"\n"},
		"comma-only setup.py":   {fileSetupPy: "setup(python_requires=\",\")\n"},
		"unmatched classifiers": {filePyproject: "[project]\nclassifiers = [\"Programming Language :: Python :: 3 :: Only\"]\n"},
		"setup.py without it":   {fileSetupPy: "from setuptools import setup\nsetup(name=\"x\")\n"},
		"comment-only version":  {filePythonVersion: "# nothing\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			d := detectOK(t, project(t, files), py)
			if d.Found || d.Constraint != nil {
				t.Errorf("found %+v", d)
			}
		})
	}
	d := detectOK(t, t.TempDir(), catalog.Tool{VersionScheme: "major.minor"})
	if d.Found {
		t.Error("tool without constraints: found")
	}
}

// setupCfgRange is the constraint text the setup.cfg fixtures spread
// over continuation lines.
const (
	setupCfgMin   = ">=3.12"
	setupCfgRange = ">=3.12, <3.15"
)

// setupPy is a setup.py constraint, which setup.cfg takes precedence
// over.
const setupPy = "setup(python_requires=\">=3.11\")\n"

const setupCfg = `[metadata]
name = example

[options]
# the floor
python_requires =
    >=3.12,
; the ceiling
    <3.15
packages = find:
`

func TestPrecedence(t *testing.T) {
	py := tool(t, "python")
	requires := "[project]\nrequires-python = \">=3.13\"\n" + classifiers3
	poetry := "[tool.poetry.dependencies]\npython = \"==3.14\"\n"
	for _, tc := range []struct {
		name   string
		files  map[string]string
		origin string
		want   []string
	}{
		{"requires-python over Poetry and classifiers",
			map[string]string{filePyproject: requires + poetry, fileSetupCfg: setupCfg},
			originRequires, pyLines[2:]},
		{"Poetry over classifiers",
			map[string]string{filePyproject: "[project]\n" + classifiers3 + poetry},
			originPoetry, []string{py314}},
		{"classifiers over setup.cfg",
			map[string]string{filePyproject: "[project]\n" + classifiers3, fileSetupCfg: setupCfg},
			originClassifiers, pyLines[:3]},
		{"setup.cfg over setup.py",
			map[string]string{fileSetupCfg: setupCfg, fileSetupPy: setupPy},
			originSetupCfg, []string{py312, py313, py314}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expect(t, detectOK(t, project(t, tc.files), py), tc.origin, tc.want)
		})
	}
}

// TestEmptyConstraintFallsThrough checks that a value that is empty, or
// nothing but commas, counts as absent, so the next rule is tried.
func TestEmptyConstraintFallsThrough(t *testing.T) {
	py := tool(t, "python")
	for _, value := range []string{"", " ", ",", " , , "} {
		t.Run(value, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{
				filePyproject: "[project]\nrequires-python = \"" + value + "\"\n" + classifiers3,
			}), py)
			expect(t, d, originClassifiers, pyLines[:3])
		})
	}
}

// TestPEP440Forms checks that detection accepts the PEP 440 forms
// Python's packaging accepts.
func TestPEP440Forms(t *testing.T) {
	py := tool(t, "python")
	for _, tc := range []struct {
		value string
		want  []string
	}{
		{">=v3.11", pyLines},
		{">=3.11,", pyLines},
		{">=1!3.11", nil},
		{"!=3.12+local", pyLines},
	} {
		t.Run(tc.value, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{
				filePyproject: "[project]\nrequires-python = \"" + tc.value + "\"\n",
			}), py)
			expect(t, d, originRequires, tc.want)
			if d.Text != tc.value {
				t.Errorf("text = %q, want %q", d.Text, tc.value)
			}
		})
	}
}

func TestPoetryLongForm(t *testing.T) {
	d := detectOK(t, project(t, map[string]string{
		filePyproject: "[tool.poetry.dependencies]\npython = {version = \"^3.11\"}\n",
	}), tool(t, "python"))
	expect(t, d, originPoetry, pyLines)
	if d.Text != "^3.11" {
		t.Errorf("text = %q", d.Text)
	}
}

func TestSetupCfg(t *testing.T) {
	d := detectOK(t, project(t, map[string]string{fileSetupCfg: setupCfg}), tool(t, "python"))
	expect(t, d, originSetupCfg, []string{py312, py313, py314})
	if d.Text != setupCfgRange {
		t.Errorf("text = %q", d.Text)
	}
	d = detectOK(t, project(t, map[string]string{
		fileSetupCfg: "[metadata]\npython_requires = >=3.15\n[options]\npython_requires: >=3.13\n",
	}), tool(t, "python"))
	expect(t, d, originSetupCfg, pyLines[2:])
	// setuptools keeps option names as written, so another case is
	// another option.
	d = detectOK(t, project(t, map[string]string{
		fileSetupCfg: "[options]\nPython_Requires: >=3.13\n",
	}), tool(t, "python"))
	if d.Found {
		t.Errorf("found %q at %q; want nothing", d.Text, d.Origin)
	}
}

// TestSetupCfgIndented follows Python's configparser: an option may be
// indented, and only a line indented deeper than its option continues
// the value.
func TestSetupCfgIndented(t *testing.T) {
	py := tool(t, "python")
	for _, tc := range []struct {
		name, cfg, text string
	}{
		{"indented option",
			"[options]\n    python_requires = >=3.12\n",
			setupCfgMin},
		{"indented option with a deeper continuation",
			"[options]\n  python_requires =\n      >=3.12,\n      <3.15\n  packages = find:\n",
			setupCfgRange},
		{"indented option followed by a sibling",
			"[options]\n    python_requires = >=3.12\n    zip_safe = False\n",
			setupCfgMin},
		{"blank line inside a value",
			"[options]\npython_requires =\n    >=3.12,\n\n    <3.15\npackages = find:\n",
			setupCfgRange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileSetupCfg: tc.cfg}), py)
			if d.Origin != originSetupCfg || d.Text != tc.text {
				t.Errorf("origin, text = %q, %q; want %q, %q", d.Origin, d.Text, originSetupCfg, tc.text)
			}
		})
	}
}

// TestSetupCfgSectionHeaders follows configparser's SECTCRE: the
// section name runs from the first "[" to the last "]", taken as
// written, and text after the header is ignored.
func TestSetupCfgSectionHeaders(t *testing.T) {
	py := tool(t, "python")
	for _, cfg := range []string{
		"[options] # Python settings\npython_requires = >=3.12\n",
		"[options]   ; note\npython_requires = >=3.12\n",
		"  [options]\npython_requires = >=3.12\n",
	} {
		d := detectOK(t, project(t, map[string]string{fileSetupCfg: cfg}), py)
		if d.Origin != originSetupCfg || d.Text != setupCfgMin {
			t.Errorf("%q: origin, text = %q, %q; want %q, %q", cfg, d.Origin, d.Text, originSetupCfg, setupCfgMin)
		}
	}
	for _, cfg := range []string{
		"[ options ]\npython_requires = >=3.12\n",
		"[options] ; see [docs]\npython_requires = >=3.12\n",
	} {
		if d := detectOK(t, project(t, map[string]string{fileSetupCfg: cfg}), py); d.Found {
			t.Errorf("%q: found %q at %q; want nothing", cfg, d.Text, d.Origin)
		}
	}
	// "[]" is not a header, so the option after it has no section,
	// which configparser refuses.
	cfg := "[]\npython_requires = >=3.12\n"
	if _, err := detect.Detect(project(t, map[string]string{fileSetupCfg: cfg}), py); err == nil || !strings.Contains(err.Error(), originSetupCfg) {
		t.Errorf("%q: err = %v; want an error naming %s", cfg, err, originSetupCfg)
	}
}

// TestSetupCfgDefaults follows configparser's DEFAULT section: it
// supplies a value to a section that exists and lacks the key, an
// explicit value wins even when empty, and repeated DEFAULT blocks
// with distinct keys merge.
func TestSetupCfgDefaults(t *testing.T) {
	py := tool(t, "python")
	for _, tc := range []struct {
		name, cfg, text string
	}{
		{"inherited from DEFAULT",
			"[DEFAULT]\npython_requires = >=3.12\n[options]\npackages = find:\n",
			setupCfgMin},
		{"inherited with a continuation",
			"[DEFAULT]\npython_requires =\n    >=3.12,\n    <3.15\n[options]\nzip_safe = False\n",
			setupCfgRange},
		{"explicit value overrides DEFAULT",
			"[DEFAULT]\npython_requires = >=3.15\n[options]\npython_requires = >=3.12\n",
			setupCfgMin},
		{"explicit value before a later DEFAULT",
			"[options]\npython_requires = >=3.12\n[DEFAULT]\npython_requires = >=3.15\n",
			setupCfgMin},
		{"repeated DEFAULT blocks with distinct keys merge",
			"[DEFAULT]\nzip_safe = False\n[options]\n[DEFAULT]\npython_requires = >=3.12\n",
			setupCfgMin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileSetupCfg: tc.cfg, fileSetupPy: setupPy}), py)
			if d.Origin != originSetupCfg || d.Text != tc.text {
				t.Errorf("origin, text = %q, %q; want %q, %q", d.Origin, d.Text, originSetupCfg, tc.text)
			}
		})
	}

	// An explicit empty value hides DEFAULT's value but, as in
	// setuptools, never hides setup.py.
	emptyCfgs := []string{
		"[DEFAULT]\npython_requires = >=3.12\n[options]\npython_requires =\n",
		"[options]\npython_requires:\n",
		"[options]\npython_requires =\n",
		// Nothing but commas declares no constraint, as empty does.
		"[options]\npython_requires = , ,\n",
	}
	t.Run("explicit empty value falls through to setup.py", func(t *testing.T) {
		for _, cfg := range emptyCfgs {
			d := detectOK(t, project(t, map[string]string{fileSetupCfg: cfg, fileSetupPy: setupPy}), py)
			if !d.Found || d.Origin != fileSetupPy || d.Text != ">=3.11" {
				t.Errorf("%q: found %v %q at %q; want >=3.11 at %s", cfg, d.Found, d.Text, d.Origin, fileSetupPy)
			}
			if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], originSetupCfg) {
				t.Errorf("%q: warnings %q; want one naming %s", cfg, d.Warnings, originSetupCfg)
			}
		}
	})
	t.Run("explicit empty value without setup.py", func(t *testing.T) {
		for _, cfg := range emptyCfgs {
			d := detectOK(t, project(t, map[string]string{fileSetupCfg: cfg}), py)
			if d.Found {
				t.Errorf("%q: found %q at %q; want nothing", cfg, d.Text, d.Origin)
			}
		}
	})

	t.Run("DEFAULT without the section", func(t *testing.T) {
		for _, cfg := range []string{
			"[DEFAULT]\npython_requires = >=3.12\n",
			"[DEFAULT]\npython_requires = >=3.12\n[metadata]\nname = example\n",
			"[default]\npython_requires = >=3.12\n[options]\n",
			"[DEFAULT]\nPYTHON_REQUIRES = >=3.12\n[options]\nzip_safe = False\n",
		} {
			d := detectOK(t, project(t, map[string]string{fileSetupCfg: cfg}), py)
			if d.Found {
				t.Errorf("%q: found %q at %q; want nothing", cfg, d.Text, d.Origin)
			}
		}
	})
}

// TestSetupCfgInterpolation follows setuptools, whose ConfigParser
// expands "%(name)s" from the section and then DEFAULT. A value that
// cannot be expanded, or a file configparser refuses, is an error
// naming the origin, and setup.py is not tried.
func TestSetupCfgInterpolation(t *testing.T) {
	py := tool(t, "python")
	for _, cfg := range []string{
		"[DEFAULT]\npython_min = 3.12\n[options]\npython_requires = >=%(python_min)s\n",
		"[options]\nmin = 3.12\npython_requires = >=%(min)s\n",
		"[DEFAULT]\na = 3.12\nb = %(a)s\n[options]\npython_requires = >=%(b)s\n",
	} {
		d := detectOK(t, project(t, map[string]string{fileSetupCfg: cfg, fileSetupPy: setupPy}), py)
		if d.Origin != originSetupCfg || d.Text != setupCfgMin {
			t.Errorf("%q: origin, text = %q, %q; want %q, %q", cfg, d.Origin, d.Text, originSetupCfg, setupCfgMin)
		}
	}
	for _, cfg := range []string{
		"[options]\npython_requires = >=%(nope)s\n",
		"[options]\npython_requires = >=3.12 %x\n",
		"[options]\na = %(b)s\nb = %(a)s\npython_requires = %(a)s\n",
		"[DEFAULT]\nMin = 3.12\n[options]\npython_requires = >=%(min)s\n",
		"[options]\npython_requires = >=3.12 # note\n",
		"[options]\npython_requires = >=3.12\npython_requires = >=3.13\n",
		"[options]\npython_requires = >=3.12\n[options]\nzip_safe = False\n",
		"[DEFAULT]\npython_requires = >=3.15\n[options]\n[DEFAULT]\npython_requires = >=3.12\n",
	} {
		d, err := detect.Detect(project(t, map[string]string{fileSetupCfg: cfg, fileSetupPy: setupPy}), py)
		if err == nil || !strings.Contains(err.Error(), originSetupCfg) {
			t.Errorf("%q: found %v at %q, err %v; want an error naming %s", cfg, d.Found, d.Origin, err, originSetupCfg)
		}
	}
}

// TestSetupCfgInterpolationLimits checks that a setup.cfg whose
// interpolation would exceed the size or reference limit is an error
// naming the origin and the limit, and that setup.py is not tried.
func TestSetupCfgInterpolationLimits(t *testing.T) {
	py := tool(t, "python")
	var b strings.Builder
	b.WriteString("[options]\na0 = >=3.12" + strings.Repeat(",>=3.12", 9) + "\n")
	for i := 1; i <= 9; i++ {
		fmt.Fprintf(&b, "a%d = %s\n", i, strings.Repeat(fmt.Sprintf("%%(a%d)s", i-1), 10))
	}
	b.WriteString("python_requires = %(a9)s\n")
	for _, tc := range []struct{ cfg, limit string }{
		{b.String(), "size limit"},
		{"[options]\ne =\npython_requires = >=3.12" + strings.Repeat("%(e)s", 10001) + "\n", "reference limit"},
	} {
		d, err := detect.Detect(project(t, map[string]string{fileSetupCfg: tc.cfg, fileSetupPy: setupPy}), py)
		if err == nil || !strings.Contains(err.Error(), originSetupCfg) || !strings.Contains(err.Error(), tc.limit) {
			t.Errorf("found %v at %q, err %v; want an error naming %s and the %s", d.Found, d.Origin, err, originSetupCfg, tc.limit)
		}
	}
}

func TestSetupPy(t *testing.T) {
	d := detectOK(t, project(t, map[string]string{
		fileSetupPy: "from setuptools import setup\n\nsetup(\n    name=\"x\",\n    python_requires=\">=3.11\",\n)\n",
	}), tool(t, "python"))
	expect(t, d, fileSetupPy, pyLines)
}

func TestPythonVersionFile(t *testing.T) {
	py := tool(t, "python")
	d := detectOK(t, project(t, map[string]string{filePythonVersion: "# pinned\n\n3.12.4\n"}), py)
	expect(t, d, filePythonVersion, []string{py312})
	if d.Text != "==3.12" {
		t.Errorf("text = %q", d.Text)
	}

	d = detectOK(t, project(t, map[string]string{filePythonVersion: "pypy3.10\n"}), py)
	if d.Found {
		t.Errorf("pypy found: %+v", d)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "pypy3.10") {
		t.Errorf("warnings = %q", d.Warnings)
	}
}

// goMod returns a go.mod declaring module example.com/x followed by
// lines.
func goMod(lines ...string) string {
	return "module example.com/x\n\n" + strings.Join(lines, "\n") + "\n"
}

// TestGo follows golang.org/x/mod's strict modfile.Parse, which the go
// command applies to a main module's go.mod.
func TestGo(t *testing.T) {
	gt := tool(t, "go")
	for _, tc := range []struct {
		name, gomod, origin, text string
		want                      []string
	}{
		{"go line", goMod("go 1.26"), originGo, go126, goLines},
		{"go patch", goMod("go 1.26.0"), originGo, "1.26.0", goLines},
		{"go later line", goMod("go 1.27"), originGo, go127, []string{go127}},
		{"go rc", goMod("go 1.27rc1"), originGo, "1.27rc1", []string{go127}},
		{"go beta", goMod("go 1.27beta1"), originGo, "1.27beta1", []string{go127}},
		{"go over toolchain", goMod("go 1.26.0", "toolchain go1.27.3"), originGo, "1.26.0", goLines},
		{"toolchain alone", goMod("toolchain go1.27.3"), originToolchain, toolchainVer, []string{go127}},
		{"toolchain custom hyphen", goMod("toolchain go1.27.3-custom"), originToolchain, toolchainVer, []string{go127}},
		{"toolchain custom plus", goMod("toolchain go1.27.3+auto"), originToolchain, toolchainVer, []string{go127}},
		{"toolchain custom rc", goMod("toolchain go1.27-rc1"), originToolchain, go127, []string{go127}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileGoMod: tc.gomod}), gt)
			expect(t, d, tc.origin, tc.want)
			if d.Text != tc.text {
				t.Errorf("text = %q, want %q", d.Text, tc.text)
			}
		})
	}
	for name, gomod := range map[string]string{
		"no directives":                goMod(),
		"toolchain default":            goMod("toolchain default"),
		"toolchain default, go absent": goMod("toolchain default", "require example.com/y v1.0.0"),
	} {
		t.Run(name, func(t *testing.T) {
			d := detectOK(t, project(t, map[string]string{fileGoMod: gomod}), gt)
			if d.Found {
				t.Errorf("found %+v", d)
			}
		})
	}
	// modfile.Parse refuses these files, so detection fails naming
	// go.mod; ParseLax would have repaired or dropped the directive.
	for name, gomod := range map[string]string{
		"broken":                    "module\n\"broken\n",
		"go extra component":        goMod("go 1.27.3.4"),
		"go hyphenated rc":          goMod("go 1.27-rc1"),
		"go major only":             goMod("go 1"),
		"go prefixed":               goMod("go go1.27"),
		"go leading zero major":     goMod("go 01.27"),
		"go leading zero minor":     goMod("go 1.027"),
		"go extra argument":         goMod("go 1.26 extra"),
		"go twice":                  goMod("go 1.26", "go 1.27"),
		"toolchain extra argument":  goMod("toolchain go1.27.3 extra"),
		"toolchain bare":            goMod("toolchain"),
		"toolchain no prefix":       goMod("toolchain 1.27.3"),
		"toolchain other ecosystem": goMod("toolchain node1.27.3"),
		"toolchain doubled prefix":  goMod("toolchain gogo1.27"),
		"go fine, toolchain extra":  goMod("go 1.26", "toolchain go1.27.3 extra"),
	} {
		t.Run(name, func(t *testing.T) {
			d, err := detect.Detect(project(t, map[string]string{fileGoMod: gomod}), gt)
			if err == nil || !strings.Contains(err.Error(), "parse "+fileGoMod) {
				t.Errorf("error = %v, want a parse error naming %s", err, fileGoMod)
			}
			if d.Found {
				t.Errorf("found %+v", d)
			}
		})
	}
	// modfile.Parse accepts these values, but they are not Go versions,
	// so detection fails naming the origin rather than finding nothing.
	for _, tc := range []struct{ name, gomod, origin string }{
		{"go suffix", goMod("go 1.27foo1"), originGo},
		{"toolchain major only", goMod("toolchain go1"), originToolchain},
		{"toolchain extra component", goMod("toolchain go1.27.3.4"), originToolchain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := detect.Detect(project(t, map[string]string{fileGoMod: tc.gomod}), gt)
			if !errors.Is(err, constraint.ErrSyntax) || !strings.Contains(err.Error(), tc.origin) {
				t.Errorf("error = %v, want ErrSyntax naming %s", err, tc.origin)
			}
			if d.Found {
				t.Errorf("found %+v", d)
			}
		})
	}
}

func TestSymlinkOutsideProject(t *testing.T) {
	outside := project(t, map[string]string{filePyproject: "[project]\nrequires-python = \">=3.11\"\n"})
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, filePyproject), filepath.Join(dir, filePyproject)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	d, err := detect.Detect(dir, tool(t, "python"))
	if err == nil {
		t.Fatalf("followed a symlink out of the project: %+v", d)
	}
	if d.Found {
		t.Errorf("found %+v", d)
	}
}

func TestFileTooLarge(t *testing.T) {
	big := "[project]\nrequires-python = \">=3.11\"\n# " + strings.Repeat("x", detect.MaxFileSize) + "\n"
	if _, err := detect.Detect(project(t, map[string]string{filePyproject: big}), tool(t, "python")); err == nil {
		t.Error("oversized file: no error")
	}
}

func TestNotRegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, filePyproject), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := detect.Detect(dir, tool(t, "python")); err == nil {
		t.Error("directory read as a file: no error")
	}
}

func TestErrors(t *testing.T) {
	py := tool(t, "python")
	for name, tc := range map[string]struct {
		pyproject, want string
	}{
		"wrong type":            {"[project]\nrequires-python = 42\n", originRequires},
		"table without version": {"[tool.poetry.dependencies]\npython = {path = \"x\"}\n", originPoetry},
		// No fallthrough to the classifiers: a broken constraint must
		// be fixed, not replaced.
		"unparsable":       {"[project]\nrequires-python = \">=banana\"\n" + classifiers3, ">=banana"},
		"classifiers type": {"[project]\nclassifiers = \"x\"\n", originClassifiers},
		"invalid toml":     {"[project\n", filePyproject},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := detect.Detect(project(t, map[string]string{filePyproject: tc.pyproject}), py)
			if err == nil {
				t.Fatalf("no error: %+v", d)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
			if d.Found {
				t.Errorf("found %+v", d)
			}
		})
	}
}

func TestUnsupportedRules(t *testing.T) {
	for name, rule := range map[string]catalog.DetectRule{
		"xpath":     {File: "pom.xml", XPath: "/project/properties/java.version"},
		"key":       {File: "gradle.properties", Key: "javaVersion"},
		"field":     {File: "package.json", Field: "engines.node"},
		"directive": {File: fileGoMod, Directive: "module"},
	} {
		t.Run(name, func(t *testing.T) {
			tl := catalog.Tool{
				VersionScheme: "major",
				Constraints:   &catalog.Constraints{Syntax: "pep440", Detect: []catalog.DetectRule{rule}},
			}
			_, err := detect.Detect(t.TempDir(), tl)
			if !errors.Is(err, detect.ErrUnsupportedRule) {
				t.Errorf("error = %v, want ErrUnsupportedRule", err)
			}
		})
	}
}

func TestBadRuleRegex(t *testing.T) {
	dir := project(t, map[string]string{fileSetupPy: "x"})
	for _, re := range []string{"(", "python_requires"} {
		tl := catalog.Tool{
			VersionScheme: "major.minor",
			Constraints: &catalog.Constraints{Syntax: "pep440", Detect: []catalog.DetectRule{
				{File: fileSetupPy, Regex: re},
			}},
		}
		if _, err := detect.Detect(dir, tl); err == nil {
			t.Errorf("regex %q: no error", re)
		}
	}
}
