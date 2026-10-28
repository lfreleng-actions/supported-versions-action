// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package catalog_test

import (
	"path/filepath"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
)

// TestInvalidFixtures parses every testdata/*/invalid-*.yaml fixture
// and checks it fails with an error at the path named by its
// "# Expect an error at:" comment.
func TestInvalidFixtures(t *testing.T) {
	for _, dir := range []string{"catalog", "overlay"} {
		files, err := filepath.Glob(filepath.Join("testdata", dir, "invalid-*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Fatalf("no fixtures in testdata/%s", dir)
		}
		for _, file := range files {
			t.Run(dir+"/"+filepath.Base(file), func(t *testing.T) {
				data, want := readFixture(t, file, "# Expect an error at: ")
				var err error
				if dir == "catalog" {
					_, err = catalog.Parse(data)
				} else {
					_, err = catalog.ParseOverlay(data)
				}
				assertErrorAt(t, err, want)
			})
		}
	}
}
