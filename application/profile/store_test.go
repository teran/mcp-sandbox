// Package profile_test covers the application-layer profile store: it scans an
// operator-authored, READ-ONLY profiles directory, parses each YAML file,
// validates it and exposes Get/List to the tools (Phase 7). There are no
// create/update/delete operations — the store is read-only by construction.
package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProfile writes a profile file into dir and returns its path.
func writeProfile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	p := filepath.Join(dir, filename)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

// validYAML is a complete, valid profile document matching the struct field
// names (lowercased, snake_case for nested keys).
const validYAML = `name: default
description: the default profile
image: docker.io/library/alpine:3.20
env:
  A: "1"
mounts:
  - source: /data
    target: /data
    read_only: true
cwd:
  target: /work
  mode: ro
network:
  mode: bridge
cgroups:
  nano_cpus: 1000000000
  memory: 536870912
  memory_swap: 536870912
  pids_limit: 256
  ulimits:
    - name: nofile
      soft: 1024
      hard: 1024
user:
  uid: 1000
  gid: 1000
sudo: never
`

func TestLoad_ScansAndParsesYAML(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	writeProfile(t, dir, "extra.yaml", strings.Replace(validYAML, "name: default", "name: extra", 1))
	// A non-profile file must be ignored.
	writeProfile(t, dir, "README.txt", "not a profile")

	st, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st == nil {
		t.Fatal("nil store")
	}
	list := st.List()
	if len(list) != 2 {
		t.Fatalf("List() = %d profiles, want 2", len(list))
	}
	// Sorted by name.
	if list[0].Name != "default" || list[1].Name != "extra" {
		t.Fatalf("List order = %v, want [default extra]", []string{list[0].Name, list[1].Name})
	}
	if list[0].Image != "docker.io/library/alpine:3.20" {
		t.Fatalf("Image = %q", list[0].Image)
	}
	if len(list[0].Mounts) != 1 || list[0].Mounts[0].Source != "/data" {
		t.Fatalf("Mounts = %+v", list[0].Mounts)
	}
	if list[0].User == nil || list[0].User.UID != 1000 {
		t.Fatalf("User = %+v, want 1000", list[0].User)
	}
}

func TestLoad_Get(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a.yml", validYAML)
	st, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := st.Get("default")
	if !ok {
		t.Fatal("Get(default) = not found")
	}
	if p.Name != "default" {
		t.Fatalf("name = %q", p.Name)
	}
	if _, ok := st.Get("missing"); ok {
		t.Fatal("Get(missing) should not be found")
	}
}

func TestLoad_DuplicateName_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a.yml", validYAML)
	writeProfile(t, dir, "b.yml", strings.Replace(validYAML, "image: docker.io/library/alpine:3.20", "image: ghcr.io/org/app:v1", 1)) // same name "default"
	if _, err := Load(dir); err == nil {
		t.Fatal("expected duplicate-name error")
	} else if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_InvalidFile_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "bad.yml", "name: broken\nimage: ::::::\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for invalid profile file")
	}
}

func TestLoad_UnparsableYAML_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "broken.yml", "name: [unclosed\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for unparsable YAML")
	}
}

func TestLoad_EmptyDir_EmptyStore(t *testing.T) {
	dir := t.TempDir()
	st, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(st.List()) != 0 {
		t.Fatalf("List() = %v, want empty", st.List())
	}
}

func TestLoad_MissingDir_EmptyStore(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("Load of missing dir should be empty, got: %v", err)
	}
	if len(st.List()) != 0 {
		t.Fatalf("List() = %v, want empty", st.List())
	}
}

// TestLoad_RejectsPathTraversalName pins the loader-hardening requirement: a
// profile whose `name` (or filename) attempts to traverse out of the profiles
// directory is rejected, so a hostile file cannot escape the read-only store.
func TestLoad_RejectsPathTraversalName(t *testing.T) {
	dir := t.TempDir()
	// A name containing path separators / ".." is a traversal attempt.
	writeProfile(t, dir, "evil.yml", strings.Replace(validYAML, "name: default", "name: ../../etc/passwd", 1))
	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for path-traversal profile name")
	} else if !strings.Contains(err.Error(), "name") && !strings.Contains(err.Error(), "travers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLoad_RejectsTraversalFilename pins that a profile whose declared name
// attempts to escape the profiles directory is never loadable, regardless of
// the file it came from. Defense in depth: the name (used as the store key) is
// the authoritative identity, so a hostile name is rejected even when the
// filename itself is a plain <name>.yml.
func TestLoad_RejectsTraversalFilename(t *testing.T) {
	dir := t.TempDir()
	// A plain filename but a hostile declared name must still be rejected.
	writeProfile(t, dir, "evil.yml", strings.Replace(validYAML, "name: default", "name: ../etc/passwd", 1))
	if _, err := Load(dir); err == nil {
		t.Fatal("expected error for traversal profile name")
	}
}

func TestStore_GetReturnsProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a.yml", validYAML)
	st, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := st.Get("default")
	if !ok {
		t.Fatal("expected profile")
	}
	_ = p // Get returns a *domainprofile.Profile
}
