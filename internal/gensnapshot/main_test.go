// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package main

import (
	"bytes"
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
	"github.com/lfreleng-actions/supported-versions-action/pkg/source"
)

// catalogRoutes answer every source of the embedded catalog.
var catalogRoutes = map[string]string{
	"/products/python/": `{"schema_version":"1.2.1","result":{"releases":[{"name":"3.1","isLts":false}]}}`,
	"/products/go/":     `{"schema_version":"1.2.1","result":{"releases":[{"name":"1.2"}]}}`,
	"/actions/python-versions/main/versions-manifest.json": `[{"version":"3.1.4","stable":true,"files":[]}]`,
	"/actions/go-versions/main/versions-manifest.json":     `[{"version":"1.2.2","stable":true,"files":[]}]`,
}

func testOptions(t *testing.T, routes map[string]string) options {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.EscapedPath()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("writing the response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return options{
		endOfLifeBaseURL: srv.URL + "/",
		manifestBaseURL:  srv.URL + "/",
		fetcher: source.Options{
			Retries: -1,
			Sleep:   func(context.Context, time.Duration) error { return nil },
		},
	}
}

func TestFailureLeavesOutputUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	old := []byte("previous contents\n")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	routes := maps.Clone(catalogRoutes)
	delete(routes, "/actions/go-versions/main/versions-manifest.json")
	if code := run([]string{"-o", path}, &stderr, testOptions(t, routes)); code == 0 {
		t.Fatal("run succeeded with a source answering 404")
	}
	if stderr.Len() == 0 {
		t.Error("run reported nothing on stderr")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("output changed to %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the output", len(entries))
	}
}

func TestUsage(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(nil, &stderr, testOptions(t, nil)); code != 2 {
		t.Errorf("run without -o = %d, want 2", code)
	}
}

func TestWritesSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte("previous contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := run([]string{"-o", path, "-timeout", "30s"}, &stderr, testOptions(t, catalogRoutes)); code != 0 {
		t.Fatalf("run = %d: %s", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := snapshot.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, _, ok := s.Lines("actions/go-versions"); !ok {
		t.Errorf("the snapshot lacks actions/go-versions:\n%s", data)
	}
}
