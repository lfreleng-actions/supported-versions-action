// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2026 The Linux Foundation

package cli

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lfreleng-actions/supported-versions-action/pkg/catalog"
	"github.com/lfreleng-actions/supported-versions-action/pkg/snapshot"
)

const previous = "previous contents\n"

func TestSnapshotWrites(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("snapshot", "--write", path)
	r.want(t, ExitOK)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := snapshot.Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, data)
	}
	cat, err := catalog.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(cat); err != nil {
		t.Errorf("Check: %v", err)
	}
	if !s.GeneratedAt.Equal(fixedNow) {
		t.Errorf("generated at %v, want the fixed clock", s.GeneratedAt)
	}
	if _, _, ok := s.Lines("actions/go-versions"); !ok {
		t.Errorf("the snapshot lacks actions/go-versions")
	}
}

func TestSnapshotFailureLeavesFile(t *testing.T) {
	h := newHarness(t)
	h.u.setFailAll(http.StatusInternalServerError)
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	r := h.run("snapshot", "--write", path)
	if r.code == ExitOK {
		t.Fatal("snapshot succeeded with every source failing")
	}
	if r.stderr == "" {
		t.Error("snapshot reported nothing on stderr")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte(previous)) {
		t.Errorf("output changed to %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the output", len(entries))
	}
}

func TestSnapshotUsage(t *testing.T) {
	h := newHarness(t)
	h.run("snapshot").want(t, ExitFailure)
	h.run("snapshot", "--write", filepath.Join(t.TempDir(), "s.json"), "extra").want(t, ExitFailure)
	h.run("--offline", "snapshot", "--write", filepath.Join(t.TempDir(), "s.json")).want(t, ExitFailure)
}
