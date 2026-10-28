// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"go.yaml.in/yaml/v3"

	catalogdata "github.com/lfreleng-actions/supported-versions-action/catalog"
)

const schemaURL = "https://github.com/lfreleng-actions/supported-versions-action/catalog/schema/catalog.schema.json"

// FieldError is one validation problem and the dotted path of the
// field it concerns, for example "tools.python.policy.floor".
type FieldError struct {
	// Path locates the field; "(root)" names the whole document.
	Path string
	// Message describes the problem.
	Message string
}

// Error formats the problem as "path: message".
func (e *FieldError) Error() string {
	return e.Path + ": " + e.Message
}

func fieldErrorf(path, format string, args ...any) *FieldError {
	return &FieldError{Path: path, Message: fmt.Sprintf(format, args...)}
}

var compiledSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(catalogdata.Schema))
	if err != nil {
		return nil, fmt.Errorf("parse catalog schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("load catalog schema: %w", err)
	}
	out := make(map[string]*jsonschema.Schema, 2)
	for name, loc := range map[string]string{
		catalogSchema: schemaURL,
		overlaySchema: schemaURL + "#/$defs/overlay",
	} {
		sch, err := c.Compile(loc)
		if err != nil {
			return nil, fmt.Errorf("compile %s schema: %w", name, err)
		}
		out[name] = sch
	}
	return out, nil
})

const (
	catalogSchema = "catalog"
	overlaySchema = "overlay"
)

// decode runs YAML bytes through the named schema and then decodes
// them strictly into out, rejecting unknown fields.
func decode(data []byte, schemaName string, out any) error {
	schemas, schemaErr := compiledSchemas()
	if schemaErr != nil {
		return schemaErr
	}
	doc, err := parseSingleDocument(data, schemaName)
	if err != nil {
		return err
	}
	value, err := yamlToJSON(doc, nil)
	if err != nil {
		return err
	}
	encoded, err := validateSchema(schemas[schemaName], schemaName, value)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", schemaName, err)
	}
	return nil
}

// parseSingleDocument parses data as exactly one YAML document and
// returns its root node. A second document, or trailing content that
// does not parse, is an error rather than silently ignored.
func parseSingleDocument(data []byte, schemaName string) (*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &FieldError{Path: rootPath, Message: "document is empty"}
		}
		return nil, fmt.Errorf("parse %s: %w", schemaName, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, &FieldError{Path: rootPath, Message: "document is empty"}
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		return doc.Content[0], nil
	case err != nil:
		return nil, fmt.Errorf("parse %s: trailing content after the first document: %w", schemaName, err)
	default:
		return nil, fieldErrorf(rootPath, "line %d: %s has a second YAML document; only one is allowed", extra.Line, schemaName)
	}
}

// checkSchema validates a Go value, such as a catalog or overlay built
// in code rather than parsed, against the named schema.
func checkSchema(schemaName string, value any) error {
	schemas, err := compiledSchemas()
	if err != nil {
		return err
	}
	_, err = validateSchema(schemas[schemaName], schemaName, value)
	return err
}

// validateSchema encodes value as JSON, validates it against sch and
// returns the encoding. Schema problems are joined FieldErrors.
func validateSchema(sch *jsonschema.Schema, schemaName string, value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", schemaName, err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("re-read %s: %w", schemaName, err)
	}
	if err := sch.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) {
			return nil, err
		}
		return nil, errors.Join(schemaErrors(ve, inst)...)
	}
	return encoded, nil
}

const rootPath = "(root)"

// maxYAMLNodes bounds the nodes a document may expand to once its
// aliases are followed, so a small document cannot fan out into an
// enormous value. Real catalogs and overlays stay far below it.
const maxYAMLNodes = 100000

// yamlConverter turns YAML nodes into JSON-compatible values. It
// follows aliases itself, since decoding into yaml.Node skips the
// typed decoder's alias checks: it refuses an alias to an anchored
// node it is still converting, and caps the total expansion.
type yamlConverter struct {
	expanding map[*yaml.Node]bool
	nodes     int
}

// yamlToJSON converts a YAML node to a JSON-compatible value. Mapping
// keys keep their source text, so a line key with a trailing zero in
// its minor is not read as a shorter number. Values keep their YAML
// types, so the schema can insist that lines are quoted strings.
func yamlToJSON(n *yaml.Node, path []string) (any, error) {
	conv := &yamlConverter{expanding: make(map[*yaml.Node]bool)}
	return conv.convert(n, path)
}

func (conv *yamlConverter) convert(n *yaml.Node, path []string) (any, error) {
	conv.nodes++
	if conv.nodes > maxYAMLNodes {
		return nil, fieldErrorf(joinPath(path), "line %d: document expands to more than %d nodes", n.Line, maxYAMLNodes)
	}
	if n.Anchor != "" {
		conv.expanding[n] = true
		defer delete(conv.expanding, n)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return conv.convert(n.Content[0], path)
	case yaml.AliasNode:
		if n.Alias == nil || conv.expanding[n.Alias] {
			return nil, fieldErrorf(joinPath(path), "line %d: alias *%s refers to a node that contains it", n.Line, n.Value)
		}
		return conv.convert(n.Alias, path)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for i, item := range n.Content {
			v, err := conv.convert(item, append(slicesClip(path), "["+strconv.Itoa(i)+"]"))
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		out := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode {
				return nil, fieldErrorf(joinPath(path), "line %d: mapping keys must be scalars", k.Line)
			}
			child := append(slicesClip(path), k.Value)
			if _, dup := out[k.Value]; dup {
				return nil, fieldErrorf(joinPath(child), "line %d: duplicate key", k.Line)
			}
			cv, err := conv.convert(v, child)
			if err != nil {
				return nil, err
			}
			out[k.Value] = cv
		}
		return out, nil
	case yaml.ScalarNode:
		return scalarValue(n, path)
	}
	return nil, fieldErrorf(joinPath(path), "line %d: unsupported YAML node", n.Line)
}

func scalarValue(n *yaml.Node, path []string) (any, error) {
	var (
		v   any
		err error
	)
	switch n.ShortTag() {
	case "!!int":
		var i int64
		err = n.Decode(&i)
		v = i
	case "!!float":
		var f float64
		err = n.Decode(&f)
		v = f
	case "!!bool":
		var b bool
		err = n.Decode(&b)
		v = b
	case "!!null":
		return nil, nil
	default:
		// Strings and timestamps, such as unquoted dates, keep their text.
		return n.Value, nil
	}
	if err != nil {
		return nil, fieldErrorf(joinPath(path), "line %d: %v", n.Line, err)
	}
	return v, nil
}

func slicesClip(s []string) []string {
	return s[:len(s):len(s)]
}

// schemaErrors flattens a schema validation error into one FieldError
// per leaf problem, sorted by path.
func schemaErrors(ve *jsonschema.ValidationError, inst any) []error {
	var fes []*FieldError
	// owner is the location of the nearest enclosing error whose
	// location the library copied, and so can be trusted.
	var walk func(e *jsonschema.ValidationError, owner []string)
	walk = func(e *jsonschema.ValidationError, owner []string) {
		switch k := e.ErrorKind.(type) {
		case *kind.Required:
			for _, m := range k.Missing {
				fes = append(fes, &FieldError{Path: instancePath(inst, append(slicesClip(e.InstanceLocation), m)), Message: "is required"})
			}
			return
		case *kind.AdditionalProperties:
			for _, p := range k.Properties {
				fes = append(fes, &FieldError{Path: instancePath(inst, append(slicesClip(e.InstanceLocation), p)), Message: "unknown field"})
			}
			return
		case *kind.PropertyNames:
			// jsonschema v6 stores a propertyNames error's location by
			// sharing the validator's path buffer, whose contents later
			// siblings overwrite; only its length survives. Recover the
			// object from the instance, searching only under the owner
			// so that the same bad key in two objects keeps two paths.
			loc := propertyOwner(inst, owner, len(e.InstanceLocation), k.Property)
			fes = append(fes, &FieldError{
				Path:    instancePath(inst, append(loc, k.Property)),
				Message: "invalid name: " + leafMessage(e.Causes),
			})
			return
		}
		if len(e.Causes) == 0 {
			fes = append(fes, &FieldError{Path: instancePath(inst, e.InstanceLocation), Message: leafMessage([]*jsonschema.ValidationError{e})})
			return
		}
		for _, c := range e.Causes {
			walk(c, e.InstanceLocation)
		}
	}
	walk(ve, nil)
	sort.SliceStable(fes, func(i, j int) bool { return fes[i].Path < fes[j].Path })
	errs := make([]error, len(fes))
	for i, fe := range fes {
		errs[i] = fe
	}
	return errs
}

// propertyOwner locates the object at the given depth that holds key,
// looking first beneath owner and then, failing that, anywhere.
func propertyOwner(inst any, owner []string, depth int, key string) []string {
	if sub, ok := valueAt(inst, owner); ok && depth >= len(owner) {
		if loc := findObjectWithKey(sub, depth-len(owner), key, slicesClip(owner)); loc != nil {
			return loc
		}
	}
	return findObjectWithKey(inst, depth, key, nil)
}

// valueAt returns the value at the JSON pointer tokens in v.
func valueAt(v any, tokens []string) (any, bool) {
	for _, tok := range tokens {
		switch val := v.(type) {
		case map[string]any:
			next, ok := val[tok]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(val) {
				return nil, false
			}
			v = val[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// findObjectWithKey returns the location of the first object at the
// given depth in v that has key, or nil when there is none.
func findObjectWithKey(v any, depth int, key string, loc []string) []string {
	switch val := v.(type) {
	case map[string]any:
		if depth == 0 {
			if _, ok := val[key]; ok {
				return append([]string{}, loc...)
			}
			return nil
		}
		for _, k := range sortedKeys(val) {
			if found := findObjectWithKey(val[k], depth-1, key, append(slicesClip(loc), k)); found != nil {
				return found
			}
		}
	case []any:
		if depth == 0 {
			return nil
		}
		for i, item := range val {
			if found := findObjectWithKey(item, depth-1, key, append(slicesClip(loc), strconv.Itoa(i))); found != nil {
				return found
			}
		}
	}
	return nil
}

// leafMessage renders the first leaf problem among errs without the
// library's location prefix, since FieldError carries the path.
func leafMessage(errs []*jsonschema.ValidationError) string {
	for len(errs) > 0 {
		e := errs[0]
		if len(e.Causes) > 0 {
			errs = e.Causes
			continue
		}
		bare := &jsonschema.ValidationError{SchemaURL: e.SchemaURL, ErrorKind: e.ErrorKind}
		return strings.TrimPrefix(bare.Error(), "at '': ")
	}
	return "invalid"
}

// instancePath renders JSON pointer tokens as a dotted path, writing
// array indices as [i] by consulting the instance.
func instancePath(inst any, tokens []string) string {
	var sb strings.Builder
	cur := inst
	for _, tok := range tokens {
		switch v := cur.(type) {
		case []any:
			sb.WriteString("[" + tok + "]")
			if i, err := strconv.Atoi(tok); err == nil && i >= 0 && i < len(v) {
				cur = v[i]
			} else {
				cur = nil
			}
			continue
		case map[string]any:
			cur = v[tok]
		default:
			cur = nil
		}
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(tok)
	}
	if sb.Len() == 0 {
		return rootPath
	}
	return sb.String()
}

// joinPath renders path tokens as a dotted path; tokens already
// written as an index, such as "[2]", attach without a dot.
func joinPath(tokens []string) string {
	if len(tokens) == 0 {
		return rootPath
	}
	var sb strings.Builder
	for i, tok := range tokens {
		if i > 0 && !strings.HasPrefix(tok, "[") {
			sb.WriteByte('.')
		}
		sb.WriteString(tok)
	}
	return sb.String()
}
