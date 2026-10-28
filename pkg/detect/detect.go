// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

// Package detect finds the version constraint a project declares for a
// tool, following the detect rules the catalog gives that tool.
//
// Detect reads files through an os.Root opened on the project
// directory, so no rule can reach outside the project, and it never
// reads more than MaxFileSize bytes of any file.
package detect

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/mod/modfile"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/constraint"
	"github.com/lfreleng-actions/supported-versions-action/pkg/release"
)

// ErrUnsupportedRule reports a detect rule this package cannot apply
// yet, such as an XPath or Key locator.
var ErrUnsupportedRule = errors.New("unsupported detect rule")

// MaxFileSize is the largest file, in bytes, Detect reads. A larger
// file is an error.
const MaxFileSize = 1 << 20

// classifiersField is where a pyproject.toml lists its trove
// classifiers.
const classifiersField = "project.classifiers"

// Detection is what Detect found.
type Detection struct {
	// Found reports that a rule yielded a constraint.
	Found bool
	// Constraint is the constraint found; nil when Found is false.
	Constraint *constraint.Constraint
	// Text is the constraint text as found. For classifiers it is the
	// matching lines, comma-separated.
	Text string
	// Origin names where the constraint was found, as the file and,
	// where the rule has one, the locator within it, separated by a
	// colon.
	Origin string
	// Warnings are problems that did not stop detection, such as a
	// version file naming something other than a version.
	Warnings []string
}

// finding is a value a rule located.
type finding struct {
	text string
	// lines, when non-nil, is an explicit set of release lines.
	lines []string
	// empty reports a value declared explicitly empty, which yields
	// no constraint but is worth a warning before later rules are
	// tried.
	empty bool
}

// Detect finds the constraint the project in dir declares for tool. It
// tries the tool's detect rules in catalog order, and the first rule
// that yields a value wins. Finding nothing is not an error: the
// result has Found false. A value that is empty, or nothing but commas
// and whitespace, declares no constraint: from any rule it counts as
// absent, and the next rule is tried, as setuptools falls back to
// setup.py when setup.cfg's python_requires is empty. A setup.cfg
// option declared explicitly empty still hides any [DEFAULT] value,
// and adds a warning.
//
// A value that is found but does not parse is an error naming its
// origin, and later rules are not tried, because a broken constraint
// should be fixed rather than silently replaced.
func Detect(dir string, tool catalog.Tool) (Detection, error) {
	var d Detection
	if tool.Constraints == nil || len(tool.Constraints.Detect) == 0 {
		return d, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return d, fmt.Errorf("detect: %w", err)
	}
	defer root.Close() //nolint:errcheck // Read-only; a close error loses nothing.

	p := &project{root: root, scheme: tool.VersionScheme, files: map[string]file{}, tomls: map[string]map[string]any{}}
	syntax := tool.Constraints.Syntax
	for _, rule := range tool.Constraints.Detect {
		origin := originOf(rule)
		f, err := p.apply(rule, origin, &d.Warnings)
		if err != nil {
			return d, fmt.Errorf("detect: %w", err)
		}
		if f == nil {
			continue
		}
		if f.empty {
			d.Warnings = append(d.Warnings, origin+": declared empty; trying later rules")
			continue
		}
		var c constraint.Constraint
		if f.lines != nil {
			c, err = constraint.NewLineSet(tool.VersionScheme, f.lines)
		} else {
			c, err = constraint.Parse(syntax, tool.VersionScheme, f.text)
		}
		if err != nil {
			return d, fmt.Errorf("detect: %s: constraint %q: %w", origin, f.text, err)
		}
		d.Found, d.Constraint, d.Text, d.Origin = true, &c, f.text, origin
		return d, nil
	}
	return d, nil
}

// originOf names where a rule looks: the file and, where the rule
// has a named locator, the locator.
func originOf(rule catalog.DetectRule) string {
	switch {
	case rule.Field != "":
		return rule.File + ":" + rule.Field
	case rule.Classifiers != "":
		return rule.File + ":" + classifiersField
	case rule.Directive != "":
		return rule.File + ":" + rule.Directive
	case rule.XPath != "":
		return rule.File + ":" + rule.XPath
	case rule.Key != "":
		return rule.File + ":" + rule.Key
	default:
		return rule.File
	}
}

// file is the cached content of a project file.
type file struct {
	data   []byte
	exists bool
}

// project reads files of one project, each at most once.
type project struct {
	root   *os.Root
	scheme string
	files  map[string]file
	tomls  map[string]map[string]any
}

// apply runs one rule. It returns nil when the rule yields nothing.
func (p *project) apply(rule catalog.DetectRule, origin string, warnings *[]string) (*finding, error) {
	switch {
	case rule.XPath != "", rule.Key != "":
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedRule, origin)
	case rule.Field != "":
		return p.field(rule, origin)
	case rule.Classifiers != "":
		return p.classifiers(rule, origin)
	case rule.Regex != "":
		return p.regex(rule, origin)
	case rule.Directive != "":
		return p.directive(rule, origin)
	default:
		return p.versionFile(rule, origin, warnings)
	}
}

// read returns the content of the file name, or exists false when it
// does not exist.
func (p *project) read(name string) (data []byte, exists bool, err error) {
	if f, ok := p.files[name]; ok {
		return f.data, f.exists, nil
	}
	data, exists, err = p.readFile(name)
	if err != nil {
		return nil, false, err
	}
	p.files[name] = file{data: data, exists: exists}
	return data, exists, nil
}

func (p *project) readFile(name string) (data []byte, exists bool, err error) {
	// The open does not block, so a FIFO cannot stall detection before
	// the check below refuses it.
	f, err := p.root.OpenFile(name, openReadFlags, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", name, err)
	}
	defer f.Close() //nolint:errcheck // Read-only; a close error loses nothing.
	fi, err := f.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", name, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, false, fmt.Errorf("read %s: not a regular file", name)
	}
	if fi.Size() > MaxFileSize {
		return nil, false, fmt.Errorf("read %s: larger than %d bytes", name, MaxFileSize)
	}
	data, err = io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", name, err)
	}
	if len(data) > MaxFileSize {
		return nil, false, fmt.Errorf("read %s: larger than %d bytes", name, MaxFileSize)
	}
	return data, true, nil
}

// parseTOML returns the parsed TOML file name, or nil when it does not
// exist.
func (p *project) parseTOML(name string) (map[string]any, error) {
	if doc, ok := p.tomls[name]; ok {
		return doc, nil
	}
	data, exists, err := p.read(name)
	if err != nil || !exists {
		return nil, err
	}
	doc := map[string]any{}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	p.tomls[name] = doc
	return doc, nil
}

// lookup follows a dotted path through nested TOML tables.
func lookup(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for key := range strings.SplitSeq(path, ".") {
		table, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = table[key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// text returns a trimmed string as a finding, and nil for one that is
// empty or holds nothing but commas, which declares no constraint.
func text(s string) *finding {
	s = strings.TrimSpace(s)
	if strings.Trim(s, ", \t\r\n") == "" {
		return nil
	}
	return &finding{text: s}
}

func (p *project) field(rule catalog.DetectRule, origin string) (*finding, error) {
	name := strings.ToLower(rule.File)
	switch {
	case strings.HasSuffix(name, ".toml"):
		return p.tomlField(rule, origin)
	case strings.HasSuffix(name, ".cfg"), strings.HasSuffix(name, ".ini"):
		return p.iniField(rule, origin)
	default:
		return nil, fmt.Errorf("%w: %s: no field reader for this file type", ErrUnsupportedRule, origin)
	}
}

func (p *project) tomlField(rule catalog.DetectRule, origin string) (*finding, error) {
	doc, err := p.parseTOML(rule.File)
	if err != nil || doc == nil {
		return nil, err
	}
	v, ok := lookup(doc, rule.Field)
	if !ok {
		return nil, nil
	}
	switch v := v.(type) {
	case string:
		return text(v), nil
	case map[string]any:
		// Poetry's long form: a table carrying the constraint as
		// its version.
		if s, ok := v["version"].(string); ok {
			return text(s), nil
		}
		return nil, fmt.Errorf("%s: table has no string version", origin)
	default:
		return nil, fmt.Errorf("%s: unsupported value of type %T", origin, v)
	}
}

// lineGroup matches a dotted number standing for a release line in a
// classifier template; the scheme then decides whether it is one.
const lineGroup = `([0-9]+(?:\.[0-9]+)*)`

func (p *project) classifiers(rule catalog.DetectRule, origin string) (*finding, error) {
	quoted := regexp.QuoteMeta(rule.Classifiers)
	placeholder := regexp.QuoteMeta("{line}")
	if !strings.Contains(quoted, placeholder) {
		return nil, fmt.Errorf("%s: classifier template %q has no {line}", origin, rule.Classifiers)
	}
	re, err := regexp.Compile("^" + strings.Replace(quoted, placeholder, lineGroup, 1) + "$")
	if err != nil {
		return nil, fmt.Errorf("%s: classifier template %q: %w", origin, rule.Classifiers, err)
	}
	doc, err := p.parseTOML(rule.File)
	if err != nil || doc == nil {
		return nil, err
	}
	v, ok := lookup(doc, classifiersField)
	if !ok {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: unsupported value of type %T", origin, v)
	}
	var lines []string
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s: unsupported classifier of type %T", origin, item)
		}
		m := re.FindStringSubmatch(strings.TrimSpace(s))
		if m != nil && release.ValidLine(m[1], p.scheme) {
			lines = append(lines, m[1])
		}
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return &finding{text: strings.Join(lines, ", "), lines: lines}, nil
}

func (p *project) regex(rule catalog.DetectRule, origin string) (*finding, error) {
	re, err := regexp.Compile(rule.Regex)
	if err != nil {
		return nil, fmt.Errorf("%s: regex: %w", origin, err)
	}
	if re.NumSubexp() < 1 {
		return nil, fmt.Errorf("%s: regex %q has no group", origin, rule.Regex)
	}
	data, exists, err := p.read(rule.File)
	if err != nil || !exists {
		return nil, err
	}
	m := re.FindSubmatch(data)
	if m == nil {
		return nil, nil
	}
	return text(string(m[1])), nil
}

func (p *project) directive(rule catalog.DetectRule, origin string) (*finding, error) {
	if rule.Directive != "go" && rule.Directive != "toolchain" {
		return nil, fmt.Errorf("%w: %s: unknown directive", ErrUnsupportedRule, origin)
	}
	data, exists, err := p.read(rule.File)
	if err != nil || !exists {
		return nil, err
	}
	// The go command reads a main module's go.mod strictly; ParseLax
	// would repair or drop malformed directives instead.
	mf, err := modfile.Parse(rule.File, data, nil)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rule.File, err)
	}
	if rule.Directive == "go" {
		if mf.Go == nil {
			return nil, nil
		}
		return text(mf.Go.Version), nil
	}
	if mf.Toolchain == nil || mf.Toolchain.Name == "default" {
		return nil, nil
	}
	name := mf.Toolchain.Name
	// A toolchain name is "go", a Go version and, for a custom
	// toolchain, a suffix starting "-" or "+".
	v, ok := strings.CutPrefix(name, "go")
	if !ok {
		return nil, fmt.Errorf("%s: %w: %q is not a Go toolchain name", origin, constraint.ErrSyntax, name)
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	return text(v), nil
}

// versionFile reads a file naming one version on its first line that
// is neither empty nor a comment, and yields an exact match on its
// release line. A first line that is not a version under the scheme,
// such as an interpreter name, yields nothing and adds a warning.
func (p *project) versionFile(rule catalog.DetectRule, origin string, warnings *[]string) (*finding, error) {
	data, exists, err := p.read(rule.File)
	if err != nil || !exists {
		return nil, err
	}
	for raw := range strings.SplitSeq(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		v, err := release.Parse(p.scheme, line)
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("%s: ignoring %q: %v", origin, line, err))
			return nil, nil
		}
		return &finding{text: "==" + v.Line}, nil
	}
	return nil, nil
}
