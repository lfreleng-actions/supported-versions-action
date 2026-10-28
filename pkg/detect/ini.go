// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
)

// The INI reader follows setuptools, which reads setup.cfg with a
// default configparser.ConfigParser whose optionxform is str: strict,
// with BasicInterpolation, case-sensitive section and option names,
// full-line "#" and ";" comments, and no inline comments.

// errINI reports a setup.cfg that configparser, and so setuptools,
// would refuse to read, or a value it would refuse to interpolate.
var errINI = errors.New("invalid INI")

// defaultSection is the section configparser, as its default_section,
// treats as supplying defaults for every other section.
const defaultSection = "DEFAULT"

// maxInterpolationDepth is configparser's MAX_INTERPOLATION_DEPTH.
const maxInterpolationDepth = 10

// configparser bounds only the depth of interpolation, so a small
// file whose options each repeat the previous reference can expand to
// gigabytes. These limits, which configparser lacks, bound the output
// and the work of one lookup.
const (
	// maxInterpolatedSize caps the bytes an interpolated value may
	// hold, checked before every append.
	maxInterpolatedSize = 64 << 10
	// maxInterpolationRefs caps the "%(name)s" substitutions of one
	// lookup, counted across the whole recursion.
	maxInterpolationRefs = 10000
)

var (
	errInterpolationSize = errors.New("interpolated value exceeds the size limit")
	errInterpolationWork = errors.New("interpolation exceeds the reference limit")
)

func (p *project) iniField(rule catalog.DetectRule, origin string) (*finding, error) {
	data, exists, err := p.read(rule.File)
	if err != nil || !exists {
		return nil, err
	}
	i := strings.LastIndex(rule.Field, ".")
	if i <= 0 || i == len(rule.Field)-1 {
		return nil, fmt.Errorf("%s: field must be section.key", origin)
	}
	v, ok, err := iniValue(string(data), rule.Field[:i], rule.Field[i+1:])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", origin, err)
	}
	if !ok {
		return nil, nil
	}
	// An explicit empty value overrides any DEFAULT, so the rule
	// yields no constraint; Detect then tries the next rule.
	if f := text(oneLine(v)); f != nil {
		return f, nil
	}
	return &finding{empty: true}, nil
}

// oneLine joins the non-blank lines of a multi-line value with a
// space, for a report.
func oneLine(v string) string {
	var parts []string
	for line := range strings.SplitSeq(v, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, " ")
}

// iniSection maps an option name to the lines of its value.
type iniSection map[string]*[]string

// iniFile is a parsed INI file.
type iniFile struct {
	defaults iniSection
	sections map[string]iniSection
}

// iniValue returns the value of key in section of an INI file, as
// setuptools reads it: interpolated, with a multi-line value's lines
// joined by newlines. The DEFAULT section supplies the value when
// section has none of its own; an explicit value in section wins,
// even an empty one. When section does not exist there is no value,
// whatever DEFAULT holds, as configparser raises NoSectionError.
//
// A file configparser would refuse, or a value it cannot
// interpolate, is an error wrapping errINI.
func iniValue(data, section, key string) (string, bool, error) {
	f, err := parseINI(data)
	if err != nil {
		return "", false, err
	}
	scope, ok := f.scope(section)
	if !ok {
		return "", false, nil
	}
	raw, ok := scope(key)
	if !ok {
		return "", false, nil
	}
	x := &expander{scope: scope, option: key}
	if err := x.expand(raw, 1); err != nil {
		return "", false, err
	}
	return x.out.String(), true, nil
}

// scope returns the lookup configparser uses for options of section:
// the section's own options, then DEFAULT's. It reports false when
// section does not exist. Each joined value is kept, so repeated
// references do not join the same lines again.
func (f *iniFile) scope(section string) (func(string) (string, bool), bool) {
	own, ok := f.sections[section]
	if section == defaultSection {
		own, ok = nil, true
	}
	if !ok {
		return nil, false
	}
	joined := map[string]string{}
	return func(key string) (string, bool) {
		if v, ok := joined[key]; ok {
			return v, true
		}
		lines, ok := own[key]
		if !ok {
			lines, ok = f.defaults[key]
		}
		if !ok {
			return "", false
		}
		v := joinValue(*lines)
		joined[key] = v
		return v, true
	}, true
}

// joinValue joins a value's lines as configparser does.
func joinValue(lines []string) string {
	return strings.TrimRightFunc(strings.Join(lines, "\n"), unicode.IsSpace)
}

// expander interpolates one option's value, enforcing the depth,
// size and reference limits across the whole recursion.
type expander struct {
	scope  func(string) (string, bool)
	option string
	out    strings.Builder
	refs   int
}

// write appends s to the output unless that would exceed
// maxInterpolatedSize.
func (x *expander) write(s string) error {
	if len(s) > maxInterpolatedSize-x.out.Len() {
		return fmt.Errorf("%w: %w: option %q: more than %d bytes", errINI, errInterpolationSize, x.option, maxInterpolatedSize)
	}
	x.out.WriteString(s)
	return nil
}

// expand interpolates value as configparser's BasicInterpolation
// does: "%%" is a literal "%", "%(name)s" is the value of option name
// in scope, expanded in turn, and any other "%" is an error.
func (x *expander) expand(value string, depth int) error {
	if depth > maxInterpolationDepth {
		return fmt.Errorf("%w: option %q: interpolation deeper than %d", errINI, x.option, maxInterpolationDepth)
	}
	for value != "" {
		i := strings.IndexByte(value, '%')
		if i < 0 {
			return x.write(value)
		}
		if err := x.write(value[:i]); err != nil {
			return err
		}
		value = value[i:]
		switch {
		case strings.HasPrefix(value, "%%"):
			if err := x.write("%"); err != nil {
				return err
			}
			value = value[2:]
		case strings.HasPrefix(value, "%("):
			end := strings.IndexByte(value, ')')
			if end < 3 || !strings.HasPrefix(value[end:], ")s") {
				return fmt.Errorf("%w: option %q: bad interpolation reference %q", errINI, x.option, value)
			}
			name := value[2:end]
			value = value[end+2:]
			if x.refs++; x.refs > maxInterpolationRefs {
				return fmt.Errorf("%w: %w: option %q: more than %d references", errINI, errInterpolationWork, x.option, maxInterpolationRefs)
			}
			v, ok := x.scope(name)
			if !ok {
				return fmt.Errorf("%w: option %q: reference to missing option %q", errINI, x.option, name)
			}
			// configparser recurses only into a value holding a "%",
			// so a plain value never counts towards the depth.
			if !strings.Contains(v, "%") {
				if err := x.write(v); err != nil {
					return err
				}
				continue
			}
			if err := x.expand(v, depth+1); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: option %q: %q must be followed by %q or %q, found %q", errINI, x.option, "%", "%", "(", value)
		}
	}
	return nil
}

// parseINI parses an INI file as configparser's strict reader does.
// An option or a section header may be indented; a line indented
// deeper than the option before it continues that option's value. A
// blank line inside a value is kept as an empty line, and full-line
// comments are skipped. A duplicate section, an option repeated in a
// section (DEFAULT blocks included), an option before any section
// and a line that is neither a header nor an option are errors.
func parseINI(data string) (*iniFile, error) {
	f := &iniFile{defaults: iniSection{}, sections: map[string]iniSection{}}
	added := map[[2]string]bool{}
	var (
		cur      iniSection
		sectName string
		value    *[]string
		level    int
	)
	data = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(data)
	lineno := 0
	for line := range strings.SplitSeq(data, "\n") {
		lineno++
		stripped := strings.TrimSpace(line)
		if stripped == "" {
			if value != nil {
				*value = append(*value, "")
			}
			continue
		}
		if stripped[0] == '#' || stripped[0] == ';' {
			continue
		}
		indent := utf8.RuneCountInString(line) - utf8.RuneCountInString(strings.TrimLeftFunc(line, unicode.IsSpace))
		if value != nil && indent > level {
			*value = append(*value, stripped)
			continue
		}
		level = indent
		if name, ok := iniHeader(stripped); ok {
			sectName, value = name, nil
			switch {
			case name == defaultSection:
				cur = f.defaults
			case f.sections[name] != nil:
				return nil, fmt.Errorf("%w: line %d: duplicate section %q", errINI, lineno, name)
			default:
				cur = iniSection{}
				f.sections[name] = cur
			}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("%w: line %d: no section header before %q", errINI, lineno, stripped)
		}
		i := strings.IndexAny(stripped, "=:")
		if i < 0 {
			return nil, fmt.Errorf("%w: line %d: not an option: %q", errINI, lineno, stripped)
		}
		name := strings.TrimRightFunc(stripped[:i], unicode.IsSpace)
		if name == "" {
			return nil, fmt.Errorf("%w: line %d: option without a name: %q", errINI, lineno, stripped)
		}
		if added[[2]string{sectName, name}] {
			return nil, fmt.Errorf("%w: line %d: duplicate option %q in section %q", errINI, lineno, name, sectName)
		}
		added[[2]string{sectName, name}] = true
		v := []string{strings.TrimSpace(stripped[i+1:])}
		cur[name], value = &v, &v
	}
	return f, nil
}

// iniHeader reports whether line is a section header and returns its
// name, as configparser's SECTCRE, `\[(?P<header>.+)\]`, matched from
// the start of the stripped line: the name is everything between the
// first "[" and the last "]", taken as written, and any text after
// the "]", such as a comment, is ignored.
func iniHeader(line string) (string, bool) {
	if !strings.HasPrefix(line, "[") {
		return "", false
	}
	end := strings.LastIndex(line, "]")
	if end < 2 {
		return "", false
	}
	return line[1:end], true
}
