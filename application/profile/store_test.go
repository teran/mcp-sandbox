// Package profile_test covers the application-layer profile store (Phase 7,
// read-on-demand): it reads + parses fresh from the operator-authored READ-ONLY
// profiles directory on EVERY Get/List call (no startup preload, no in-memory
// map). It is read-only by construction and fail-closed (S04). There are no
// create/update/delete operations.
package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
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

// --- Get ---

// TestGet_ShellParsesFromYAML pins that the `shell:` YAML key parses into the
// domain profile's Shell field (defaulted to "bash" when unset).
func TestGet_ShellParsesFromYAML(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	writeProfile(t, dir, "sh.yml", strings.Replace(validYAML, "name: default", "name: shp\nshell: /bin/sh", 1))
	st := New(dir)

	p, err := st.Get("shp")
	if err != nil {
		t.Fatalf("Get(shp): %v", err)
	}
	if p.Shell != "/bin/sh" {
		t.Fatalf("Shell = %q, want \"/bin/sh\" from YAML", p.Shell)
	}
}

func TestGet_ReturnsProfile(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	st := New(dir)

	p, err := st.Get("default")
	if err != nil {
		t.Fatalf("Get(default): %v", err)
	}
	if p == nil || p.Name != "default" {
		t.Fatalf("Get(default) = %+v, want profile named default", p)
	}
	if p.Image != "docker.io/library/alpine:3.20" {
		t.Fatalf("Image = %q", p.Image)
	}
	if len(p.Mounts) != 1 || p.Mounts[0].Source != "/data" {
		t.Fatalf("Mounts = %+v", p.Mounts)
	}
	if p.User == nil || p.User.UID != 1000 {
		t.Fatalf("User = %+v, want 1000", p.User)
	}
}

func TestGet_UnknownProfile_ErrProfileNotFound(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	st := New(dir)

	if _, err := st.Get("missing"); !errors.Is(err, domainprofile.ErrProfileNotFound) {
		t.Fatalf("Get(missing) err = %v, want ErrProfileNotFound", err)
	}
}

func TestGet_EmptyDir_ErrProfileNotFound(t *testing.T) {
	st := New(t.TempDir())
	if _, err := st.Get("x"); !errors.Is(err, domainprofile.ErrProfileNotFound) {
		t.Fatalf("Get err = %v, want ErrProfileNotFound", err)
	}
}

func TestGet_MissingDir_ErrProfileNotFound(t *testing.T) {
	st := New(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := st.Get("x"); !errors.Is(err, domainprofile.ErrProfileNotFound) {
		t.Fatalf("Get err = %v, want ErrProfileNotFound", err)
	}
}

// TestGet_ReadsOnDemand_ReflectsEdits pins the core read-on-demand contract: a
// single Store reflects filesystem edits on the NEXT call — no restart, no
// in-memory snapshot. Overwrite → new value; add → appears; delete → not found.
func TestGet_ReadsOnDemand_ReflectsEdits(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	st := New(dir)

	// Overwrite → new value on the next call.
	writeProfile(t, dir, "default.yml", strings.Replace(validYAML, "image: docker.io/library/alpine:3.20", "image: ghcr.io/org/other:v2", 1))
	p, err := st.Get("default")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if p.Image != "ghcr.io/org/other:v2" {
		t.Fatalf("Image = %q, want ghcr.io/org/other:v2 (overwrite must be visible on-demand)", p.Image)
	}

	// Add → appears.
	writeProfile(t, dir, "extra.yaml", strings.Replace(validYAML, "name: default", "name: extra", 1))
	if _, err := st.Get("extra"); err != nil {
		t.Fatalf("Get(extra) after add: %v", err)
	}

	// Delete → not found.
	if err := os.Remove(filepath.Join(dir, "extra.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("extra"); !errors.Is(err, domainprofile.ErrProfileNotFound) {
		t.Fatalf("Get(extra) after delete err = %v, want ErrProfileNotFound", err)
	}
}

// --- List ---

func TestList_ScansAndParsesYAML_Sorted(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	writeProfile(t, dir, "extra.yaml", strings.Replace(validYAML, "name: default", "name: extra", 1))
	// A non-profile file must be ignored.
	writeProfile(t, dir, "README.txt", "not a profile")

	st := New(dir)
	list, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
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
}

func TestList_EmptyDir_Empty(t *testing.T) {
	st := New(t.TempDir())
	list, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List() = %v, want empty", list)
	}
}

func TestList_MissingDir_Empty(t *testing.T) {
	st := New(filepath.Join(t.TempDir(), "does-not-exist"))
	list, err := st.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("List() = %v, want empty", list)
	}
}

// --- Error taxonomy (fail-closed, S04) ---

func TestGet_DuplicateName_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a.yml", validYAML)
	writeProfile(t, dir, "b.yml", strings.Replace(validYAML, "image: docker.io/library/alpine:3.20", "image: ghcr.io/org/app:v1", 1)) // same name "default"
	st := New(dir)
	if _, err := st.Get("default"); err == nil {
		t.Fatal("expected duplicate-name error")
	} else if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestList_DuplicateName_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a.yml", validYAML)
	writeProfile(t, dir, "b.yml", strings.Replace(validYAML, "image: docker.io/library/alpine:3.20", "image: ghcr.io/org/app:v1", 1)) // same name "default"
	st := New(dir)
	if _, err := st.List(); err == nil {
		t.Fatal("expected duplicate-name error")
	} else if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGet_InvalidFile_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "bad.yml", "name: broken\nimage: ::::::\n")
	st := New(dir)
	if _, err := st.Get("default"); err == nil {
		t.Fatal("expected error for invalid profile file")
	}
}

func TestList_UnparsableYAML_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "broken.yml", "name: [unclosed\n")
	st := New(dir)
	if _, err := st.List(); err == nil {
		t.Fatal("expected error for unparsable YAML")
	}
}

// TestGet_RejectsTraversalName pins the loader hardening: a profile whose
// declared `name` attempts to traverse out of the profiles directory is
// rejected when scanned (fail-closed), so a hostile file cannot escape.
func TestGet_RejectsTraversalName(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "evil.yml", strings.Replace(validYAML, "name: default", "name: ../../etc/passwd", 1))
	st := New(dir)
	if _, err := st.Get("default"); err == nil {
		t.Fatal("expected error for path-traversal profile name")
	} else if !strings.Contains(err.Error(), "name") && !strings.Contains(err.Error(), "travers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGet_RejectsInvalidLookupName pins the up-front validName guard: a
// malformed/traversal LOOKUP name is rejected with ErrInvalidName WITHOUT ever
// being used as a filesystem path (the lookup key is never a path).
func TestGet_RejectsInvalidLookupName(t *testing.T) {
	st := New(t.TempDir())
	for _, name := range []string{"../../etc/passwd", "../x", "a/b", "a\\b", ".", "..", "UPPER", "with space"} {
		if _, err := st.Get(name); !errors.Is(err, domainprofile.ErrInvalidName) {
			t.Fatalf("Get(%q) err = %v, want ErrInvalidName", name, err)
		}
	}
}

// TestReadDirOnFile_Error pins the non-NotExist ReadDir failure branch: when
// the profile path is a regular file (not a directory), os.ReadDir returns an
// error that is not IsNotExist, so Get/List must surface it (fail-closed)
// rather than treat it as empty/not-found.
func TestReadDirOnFile_Error(t *testing.T) {
	file := writeProfile(t, t.TempDir(), "x.txt", "hello")
	st := New(file)
	if _, err := st.Get("x"); err == nil {
		t.Fatal("Get on a regular file path should return an error")
	}
	if _, err := st.List(); err == nil {
		t.Fatal("List on a regular file path should return an error")
	}
}

// TestConcurrent_GetList_RaceSafe pins the concurrency contract (X05): the
// read-on-demand store keeps no shared mutable state and no mutex, so
// concurrent Get+List must stay clean under -race.
func TestConcurrent_GetList_RaceSafe(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	writeProfile(t, dir, "extra.yaml", strings.Replace(validYAML, "name: default", "name: extra", 1))
	st := New(dir)

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := st.Get("default"); err != nil {
				t.Errorf("Get: %v", err)
			}
			if _, err := st.List(); err != nil {
				t.Errorf("List: %v", err)
			}
		}()
	}
	wg.Wait()
}

// --- Friendly cgroups unit translation (SPEC §7.11) ---

func TestGet_FriendlyCgroupsUnits(t *testing.T) {
	const yaml = `name: friendly
description: friendly units profile
image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  cpu_cores: 4
  memory_gb: 8
  pids_limit: 256
sudo: never
`
	dir := t.TempDir()
	writeProfile(t, dir, "friendly.yml", yaml)

	st := New(dir)
	p, err := st.Get("friendly")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := p.Cgroups.NanoCPUs; got != int64(4*1_000_000_000) {
		t.Fatalf("NanoCPUs = %d, want %d", got, int64(4*1_000_000_000))
	}
	if got := p.Cgroups.Memory; got != int64(8*1024*1024*1024) {
		t.Fatalf("Memory = %d, want %d", got, int64(8*1024*1024*1024))
	}
	if got := p.Cgroups.MemorySwap; got != p.Cgroups.Memory {
		t.Fatalf("MemorySwap = %d, want %d (defaulted to Memory)", got, p.Cgroups.Memory)
	}
	if got := p.Cgroups.PidsLimit; got != 256 {
		t.Fatalf("PidsLimit = %d, want 256", got)
	}
}

func TestGet_FriendlyUnits_RawTakePrecedence(t *testing.T) {
	const yaml = `name: raw-wins
description: raw precedence profile
image: docker.io/library/alpine:3.20
network:
  mode: bridge
cgroups:
  cpu_cores: 4
  memory_gb: 8
  nano_cpus: 2000000000
  memory: 1073741824
  pids_limit: 256
sudo: never
`
	dir := t.TempDir()
	writeProfile(t, dir, "raw.yml", yaml)

	st := New(dir)
	p, err := st.Get("raw-wins")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := p.Cgroups.NanoCPUs; got != int64(2_000_000_000) {
		t.Fatalf("NanoCPUs = %d, want %d (raw precedence)", got, int64(2_000_000_000))
	}
	if got := p.Cgroups.Memory; got != int64(1_073_741_824) {
		t.Fatalf("Memory = %d, want %d (raw precedence)", got, int64(1_073_741_824))
	}
	if got := p.Cgroups.MemorySwap; got != p.Cgroups.Memory {
		t.Fatalf("MemorySwap = %d, want %d (defaulted to Memory)", got, p.Cgroups.Memory)
	}
}

func TestCgroupsToDomain_MemorySwapPreserved(t *testing.T) {
	cg := (yamlCgroups{
		NanoCPUs:   0,
		Memory:     0,
		MemorySwap: 0,
		CPUCores:   2,
		MemoryGB:   4,
	}).toDomain()
	// Swap defaulted to Memory because no raw memory_swap was provided.
	if cg.MemorySwap != cg.Memory {
		t.Fatalf("MemorySwap = %d, want defaulted to Memory %d", cg.MemorySwap, cg.Memory)
	}

	cg2 := (yamlCgroups{
		NanoCPUs:   0,
		Memory:     0,
		MemorySwap: 8 * 1024 * 1024 * 1024, // operator set a swap larger than memory
		CPUCores:   2,
		MemoryGB:   4,
	}).toDomain()
	// Explicit memory_swap must survive the defaulting.
	if cg2.MemorySwap == cg2.Memory {
		t.Fatalf("explicit MemorySwap = %d was overwritten to equal Memory %d", cg2.MemorySwap, cg2.Memory)
	}
	if want := int64(8 * 1024 * 1024 * 1024); cg2.MemorySwap != want {
		t.Fatalf("MemorySwap = %d, want preserved %d", cg2.MemorySwap, want)
	}
	if cg2.Memory != int64(4*1024*1024*1024) {
		t.Fatalf("Memory = %d, want 4 GiB", cg2.Memory)
	}
}

func TestCgroupsToDomain_OnlyCPUCores(t *testing.T) {
	cg := (yamlCgroups{CPUCores: 3}).toDomain()
	if got := cg.NanoCPUs; got != int64(3*1_000_000_000) {
		t.Fatalf("NanoCPUs = %d, want %d", got, int64(3*1_000_000_000))
	}
	if cg.Memory != 0 {
		t.Fatalf("Memory = %d, want 0 when memory_gb unset", cg.Memory)
	}
	if cg.MemorySwap != 0 {
		t.Fatalf("MemorySwap = %d, want 0 when Memory is 0", cg.MemorySwap)
	}
}

func TestCgroupsToDomain_RawAndFriendlyMixed(t *testing.T) {
	cg := (yamlCgroups{
		NanoCPUs: 0,
		Memory:   2 * 1024 * 1024 * 1024,
		CPUCores: 2,
		MemoryGB: 8, // ignored: raw Memory is already set
	}).toDomain()
	if cg.NanoCPUs != int64(2*1_000_000_000) {
		t.Fatalf("NanoCPUs = %d, want filled from cpu_cores", cg.NanoCPUs)
	}
	if cg.Memory != int64(2*1024*1024*1024) {
		t.Fatalf("Memory = %d, want raw memory", cg.Memory)
	}
	if cg.MemorySwap != cg.Memory {
		t.Fatalf("MemorySwap = %d, want defaulted to raw Memory %d", cg.MemorySwap, cg.Memory)
	}
}
