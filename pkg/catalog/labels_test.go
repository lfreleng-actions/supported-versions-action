// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package catalog_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
)

const (
	labelStable = "stable"
	firstLabel  = "labels[0]"
)

// labelCases are policy label lists and the path of the one error
// each must draw, or "" for a list that is valid. The path is relative
// to the policy.
var labelCases = map[string]struct {
	labels []string
	want   string
}{
	"valid":            {[]string{labelStable, "nightly", "lts-latest"}, ""},
	"valid 32 chars":   {[]string{"a" + strings.Repeat("b", 31)}, ""},
	"valid separators": {[]string{"lts_latest", "beta.next"}, ""},
	"empty":            {[]string{labelStable, ""}, "labels[1]"},
	"upper case":       {[]string{"Stable"}, firstLabel},
	"control chars":    {[]string{"stable\n::error::x"}, firstLabel},
	"line":             {[]string{"3.12"}, firstLabel},
	"prefixed major":   {[]string{labelStable, "v3"}, "labels[1]"},
	"compact python":   {[]string{"py312"}, firstLabel},
	"33 chars":         {[]string{"a" + strings.Repeat("b", 32)}, firstLabel},
	"duplicate":        {[]string{labelStable, "beta", labelStable}, "labels[2]"},
}

// TestCatalogPolicyLabels checks the schema and the semantic checks
// accept channel labels and reject anything else, under both schemes.
func TestCatalogPolicyLabels(t *testing.T) {
	for _, scheme := range []string{"major", "major.minor"} {
		for name, tc := range labelCases {
			t.Run(scheme+"/"+name, func(t *testing.T) {
				data := fmt.Sprintf(`schema: 1
tools:
  demo:
    name: Demo
    version_scheme: %s
    matrix_key: demo-version
    sources: [{type: endoflife, product: demo}]
    policy: {rule: all, labels: %s}
`, scheme, flowList(t, tc.labels))
				c, err := catalog.Parse([]byte(data))
				assertLabelErrors(t, err, "tools.demo.policy.", tc.want)
				if tc.want == "" && !reflect.DeepEqual(c.Tools["demo"].Policy.Labels, tc.labels) {
					t.Errorf("labels = %q, want %q", c.Tools["demo"].Policy.Labels, tc.labels)
				}
			})
		}
	}
}

// TestOverlayPolicyLabels checks an overlay's labels meet the same
// rules once parsed and applied.
func TestOverlayPolicyLabels(t *testing.T) {
	base := loadValid(t)
	for name, tc := range labelCases {
		t.Run(name, func(t *testing.T) {
			data := fmt.Sprintf("schema: 1\ntools:\n  python:\n    policy: {labels: %s}\n", flowList(t, tc.labels))
			o, err := catalog.ParseOverlay([]byte(data))
			if err == nil {
				var got *catalog.Catalog
				got, err = base.Apply(o)
				if err == nil && !reflect.DeepEqual(got.Tools[python].Policy.Labels, tc.labels) {
					t.Errorf("labels = %q, want %q", got.Tools[python].Policy.Labels, tc.labels)
				}
			}
			assertLabelErrors(t, err, "tools.python.policy.", tc.want)
		})
	}
}

// flowList renders labels as a YAML flow sequence of quoted strings.
func flowList(t *testing.T, labels []string) string {
	t.Helper()
	b, err := json.Marshal(labels)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertLabelErrors checks err holds exactly one error, at prefix+want,
// or none when want is "".
func assertLabelErrors(t *testing.T, err error, prefix, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if got := errorPaths(err); !reflect.DeepEqual(got, []string{prefix + want}) {
		t.Fatalf("error paths = %v, want [%s%s]; error: %v", got, prefix, want, err)
	}
}
