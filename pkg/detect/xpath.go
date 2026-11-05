// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package detect

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
)

// xpathStep is one element name of the absolute paths the xpath rule
// supports.
var xpathStep = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._-]*$`)

// propertyRef is a Maven property reference such as ${java.version}.
var propertyRef = regexp.MustCompile(`\$\{([^${}]+)\}`)

// propertiesElement is the child of the root element that holds a
// Maven POM's properties.
const propertiesElement = "properties"

// xpathSteps splits an absolute element path such as
// /project/properties/maven.compiler.release into its element names. It
// rejects anything else: relative paths, "//", attributes, predicates
// and functions.
func xpathSteps(rule catalog.DetectRule, origin string) ([]string, error) {
	rest, ok := strings.CutPrefix(rule.XPath, "/")
	if !ok {
		return nil, fmt.Errorf("%w: %s: only absolute element paths are supported", ErrUnsupportedRule, origin)
	}
	steps := strings.Split(rest, "/")
	for _, s := range steps {
		if !xpathStep.MatchString(s) {
			return nil, fmt.Errorf("%w: %s: only absolute element paths are supported", ErrUnsupportedRule, origin)
		}
	}
	return steps, nil
}

// xmlDoc is what one pass over an XML file collected: the text of the
// first element at the path sought, and the root's properties.
type xmlDoc struct {
	value      string
	found      bool
	properties map[string]string
}

// xpath reads the text of the first element at an absolute element
// path in an XML file. Elements match by local name within the root
// element's namespace, so a Maven POM's default namespace needs no
// prefix in the path. Maven ${name} references are replaced from the
// root's <properties>; one that cannot be resolved there yields
// nothing and adds a warning, so later rules are tried.
func (p *project) xpath(rule catalog.DetectRule, origin string, warnings *[]string) (*finding, error) {
	steps, err := xpathSteps(rule, origin)
	if err != nil {
		return nil, err
	}
	data, exists, err := p.read(rule.File)
	if err != nil || !exists {
		return nil, err
	}
	doc, err := scanXML(data, steps)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rule.File, err)
	}
	if !doc.found {
		return nil, nil
	}
	v, err := resolveProperties(doc.value, doc.properties)
	if err != nil {
		*warnings = append(*warnings, fmt.Sprintf("%s: %v; trying later rules", origin, err))
		return nil, nil
	}
	return text(v), nil
}

// scanXML walks the document once, recording the direct text of the
// first element at steps and of each child of the root's
// <properties>.
func scanXML(data []byte, steps []string) (xmlDoc, error) {
	doc := xmlDoc{properties: map[string]string{}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	var (
		space string
		path  []string
		// match is true while inside the element sought; within it,
		// only its own text is kept.
		match    bool
		depth    int
		buf      strings.Builder
		property strings.Builder
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return xmlDoc{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(path) == 0 {
				space = t.Name.Space
			}
			name := t.Name.Local
			if t.Name.Space != space {
				// An element of another namespace matches no step.
				name = ""
			}
			path = append(path, name)
			depth = len(path)
			if !doc.found && !match && equalSteps(path, steps) {
				match = true
				buf.Reset()
			}
			property.Reset()
		case xml.CharData:
			if match && len(path) == len(steps) {
				buf.Write(t)
			}
			property.Write(t)
		case xml.EndElement:
			if len(path) == 3 && path[1] == propertiesElement && path[2] != "" && depth == 3 {
				if _, seen := doc.properties[path[2]]; !seen {
					doc.properties[path[2]] = strings.TrimSpace(property.String())
				}
			}
			if match && len(path) == len(steps) {
				doc.value, doc.found, match = buf.String(), true, false
			}
			path = path[:len(path)-1]
			property.Reset()
		}
	}
	return doc, nil
}

// charsetReader decodes the single-byte encodings XML files declare
// besides UTF-8, such as the ISO-8859-1 of many older POMs. Each byte
// becomes the code point of the same value: exact for ISO-8859-1 and
// US-ASCII, and for windows-1252 exact outside 0x80-0x9F, which
// matters little since detection reads ASCII release numbers. Other
// encodings are an error.
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "us-ascii", "ascii", "windows-1252", "cp1252":
	default:
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	runes := make([]rune, len(data))
	for i, b := range data {
		runes[i] = rune(b)
	}
	return strings.NewReader(string(runes)), nil
}

func equalSteps(path, steps []string) bool {
	if len(path) != len(steps) {
		return false
	}
	for i := range path {
		if path[i] != steps[i] {
			return false
		}
	}
	return true
}

// resolveProperties replaces ${name} references in v from properties,
// expanding each property's value in turn, since a property may itself
// refer to another. A reference to a property it lacks, a cycle of
// properties, or an expansion beyond maxInterpolatedSize bytes or
// maxInterpolationRefs references is an error, raised before the
// output grows past the size limit.
func resolveProperties(v string, properties map[string]string) (string, error) {
	x := propertyExpander{properties: properties, active: map[string]bool{}}
	if err := x.expand(v); err != nil {
		return "", err
	}
	return x.out.String(), nil
}

// propertyExpander expands Maven property references, enforcing the
// size and reference limits across the whole recursion.
type propertyExpander struct {
	properties map[string]string
	// active holds the properties being expanded, to detect a cycle.
	active map[string]bool
	out    strings.Builder
	refs   int
}

func (x *propertyExpander) write(s string) error {
	if len(s) > maxInterpolatedSize-x.out.Len() {
		return fmt.Errorf("%w: more than %d bytes", errInterpolationSize, maxInterpolatedSize)
	}
	x.out.WriteString(s)
	return nil
}

func (x *propertyExpander) expand(v string) error {
	for v != "" {
		m := propertyRef.FindStringSubmatchIndex(v)
		if m == nil {
			return x.write(v)
		}
		if err := x.write(v[:m[0]]); err != nil {
			return err
		}
		name := v[m[2]:m[3]]
		v = v[m[1]:]
		if x.refs++; x.refs > maxInterpolationRefs {
			return fmt.Errorf("%w: more than %d references", errInterpolationWork, maxInterpolationRefs)
		}
		pv, ok := x.properties[name]
		if !ok {
			return fmt.Errorf("cannot resolve ${%s} from the file's properties", name)
		}
		if x.active[name] {
			return fmt.Errorf("properties form a cycle through ${%s}", name)
		}
		x.active[name] = true
		if err := x.expand(pv); err != nil {
			return err
		}
		delete(x.active, name)
	}
	return nil
}
