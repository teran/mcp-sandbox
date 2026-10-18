// Package profile covers the application-layer profile store (Phase 7,
// read-on-demand): it reads + parses fresh from the operator-authored READ-ONLY
// profiles directory on EVERY Get/List call (no startup preload, no in-memory
// map). It is read-only by construction and fail-closed (S04).
//
// After the driver-registry migration (Slices 2+3) a profile document is
// DRIVER-AGNOSTIC: a required `driver` plus an opaque `options:` yaml.Node. The
// store parses `options:` into Profile.Options without interpreting it — the
// concrete driver builders decode it. The store rejects only an empty/auto/
// malformed `driver` at scan; an unknown but well-formed driver token passes
// and fails later at USE time (registry Get -> ErrUnknownDriver). The friendly
// cgroup unit translation and driver-branch validation moved into the built-in
// builders, so they are no longer asserted here.
package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
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

// validYAML is a complete, valid profile document matching the new driver-aware
// schema: a required `driver` plus an opaque `options:` block (the store never
// interprets it — the docker builder does).
const validYAML = `name: default
description: the default profile
driver: docker
options:
  image: docker.io/library/alpine:3.20
  env:
    A: "1"
  mounts:
    - source: /data
      target: /data
      read_only: true
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
cwd:
  target: /work
  mode: ro
`

// dockerOptionKeys is the local mirror of the docker builder's option keys,
// used to decode the opaque node and assert the store carried it verbatim.
type dockerOptionKeys struct {
	Image  string            `yaml:"image"`
	Env    map[string]string `yaml:"env"`
	Mounts []struct {
		Source string `yaml:"source"`
	} `yaml:"mounts"`
	User *struct {
		UID uint32 `yaml:"uid"`
	} `yaml:"user"`
}

// decodeDockerOptions decodes a profile's opaque Options node into the local
// docker option mirror.
func decodeDockerOptions(t *testing.T, p *domainprofile.Profile) dockerOptionKeys {
	t.Helper()
	var out dockerOptionKeys
	if err := p.Options.Node().Decode(&out); err != nil {
		t.Fatalf("decode options: %v", err)
	}

	return out
}

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
	if p.Driver != spec.DriverDocker {
		t.Fatalf("Driver = %q, want docker", p.Driver)
	}
	// The opaque options carry the docker options verbatim.
	opts := decodeDockerOptions(t, p)
	if opts.Image != "docker.io/library/alpine:3.20" {
		t.Fatalf("Image = %q", opts.Image)
	}
	if len(opts.Mounts) != 1 || opts.Mounts[0].Source != "/data" {
		t.Fatalf("Mounts = %+v", opts.Mounts)
	}
	if opts.User == nil || opts.User.UID != 1000 {
		t.Fatalf("User = %+v, want uid 1000", opts.User)
	}
	if opts.Env["A"] != "1" {
		t.Fatalf("Env = %+v", opts.Env)
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
	writeProfile(t, dir, "default.yml",
		strings.Replace(validYAML, "image: docker.io/library/alpine:3.20", "image: ghcr.io/org/other:v2", 1))
	p, err := st.Get("default")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if got := decodeDockerOptions(t, p).Image; got != "ghcr.io/org/other:v2" {
		t.Fatalf("Image = %q, want ghcr.io/org/other:v2 (overwrite must be visible on-demand)", got)
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
	if list[0].Driver != spec.DriverDocker {
		t.Fatalf("Driver = %q, want docker", list[0].Driver)
	}
	if got := decodeDockerOptions(t, list[0]).Image; got != "docker.io/library/alpine:3.20" {
		t.Fatalf("Image = %q", got)
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

// --- Driver schema (empty/auto/malformed rejected at scan) ---

// TestGet_DriverRequired_Error pins that a profile omitting `driver` is
// rejected at scan (the driver is REQUIRED; there is no auto resolution).
func TestGet_DriverRequired_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "no.yml", `name: nodrv
description: missing driver
options:
  image: alpine
`)
	st := New(dir)
	if _, err := st.Get("nodrv"); err == nil {
		t.Fatal("expected error for profile with no driver")
	} else if !strings.Contains(err.Error(), "driver") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGet_DriverAuto_Error pins that `driver: auto` is rejected at scan (the
// profile is now explicit; auto was the pre-migration resolver).
func TestGet_DriverAuto_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "auto.yml", `name: autop
description: auto driver
driver: auto
options:
  image: alpine
`)
	st := New(dir)
	if _, err := st.Get("autop"); err == nil {
		t.Fatal("expected error for driver: auto")
	} else if !strings.Contains(err.Error(), "driver") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGet_DriverMalformed_Error pins that a malformed driver token is rejected
// at scan.
func TestGet_DriverMalformed_Error(t *testing.T) {
	for _, d := range []string{"Bad/Driver", "has space", "UPPER"} {
		dir := t.TempDir()
		writeProfile(t, dir, "bad.yml", "name: badp\ndescription: bad driver\ndriver: "+d+"\noptions:\n  image: alpine\n")
		st := New(dir)
		if _, err := st.Get("badp"); err == nil {
			t.Errorf("driver %q: expected scan error", d)
		}
	}
}

// TestGet_UnknownWellFormedDriver_Accepted pins the fail-at-USE-time contract:
// a well-formed but unknown driver token passes the store scan (the store
// rejects only empty/auto/malformed); it fails later at the registry Get.
func TestGet_UnknownWellFormedDriver_Accepted(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "bwrap.yml", `name: bwrapp
description: a bwrap profile
driver: bwrap
options:
  anything: here
`)
	st := New(dir)
	p, err := st.Get("bwrapp")
	if err != nil {
		t.Fatalf("Get(bwrapp): %v", err)
	}
	if p.Driver != spec.Driver("bwrap") {
		t.Fatalf("Driver = %q, want bwrap preserved", p.Driver)
	}
	if p.Options.Node() == nil {
		t.Fatal("Options node must be carried for a well-formed unknown driver")
	}
}

// TestGet_SeatbeltOptionsParsed pins that a `driver: seatbelt` profile with
// `options:` (sbpl/mach_lookups/sysctls/env) parses into Profile.Options as an
// opaque node, with Driver seatbelt.
func TestGet_SeatbeltOptionsParsed(t *testing.T) {
	const yaml = `name: mac
description: a seatbelt profile
driver: seatbelt
options:
  sbpl: |
    (import "system.sb")
    (allow process-fork)
  mach_lookups:
    - com.apple.system.logger
  sysctls:
    - kern.osversion
  env:
    MY_VAR: hello-from-profile
cwd:
  mode: ro
`
	dir := t.TempDir()
	writeProfile(t, dir, "mac.yml", yaml)
	st := New(dir)

	p, err := st.Get("mac")
	if err != nil {
		t.Fatalf("Get(mac): %v", err)
	}
	if p.Driver != spec.DriverSeatbelt {
		t.Fatalf("Driver = %q, want seatbelt", p.Driver)
	}
	var opts struct {
		SBPL        string            `yaml:"sbpl"`
		MachLookups []string          `yaml:"mach_lookups"`
		Sysctls     []string          `yaml:"sysctls"`
		Env         map[string]string `yaml:"env"`
	}
	if err := p.Options.Node().Decode(&opts); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(opts.SBPL, "(allow process-fork)") {
		t.Fatalf("SBPL = %q, want operator SBPL", opts.SBPL)
	}
	if len(opts.MachLookups) != 1 || opts.MachLookups[0] != "com.apple.system.logger" {
		t.Fatalf("MachLookups = %+v", opts.MachLookups)
	}
	if len(opts.Sysctls) != 1 || opts.Sysctls[0] != "kern.osversion" {
		t.Fatalf("Sysctls = %+v", opts.Sysctls)
	}
	if opts.Env["MY_VAR"] != "hello-from-profile" {
		t.Fatalf("Env = %+v, want profile env", opts.Env)
	}
}

// --- Error taxonomy (fail-closed, S04) ---

func TestGet_DuplicateName_Error(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a.yml", validYAML)
	writeProfile(t, dir, "b.yml",
		strings.Replace(validYAML, "image: docker.io/library/alpine:3.20", "image: ghcr.io/org/app:v1", 1)) // same name "default"
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
	writeProfile(t, dir, "b.yml",
		strings.Replace(validYAML, "image: docker.io/library/alpine:3.20", "image: ghcr.io/org/app:v1", 1)) // same name "default"
	st := New(dir)
	if _, err := st.List(); err == nil {
		t.Fatal("expected duplicate-name error")
	} else if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGet_InvalidFile_Error(t *testing.T) {
	dir := t.TempDir()
	// No `driver` — the required-driver Validate check rejects it.
	writeProfile(t, dir, "bad.yml", "name: broken\n")
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
		wg.Go(func() {
			if _, err := st.Get("default"); err != nil {
				t.Errorf("Get: %v", err)
			}
			if _, err := st.List(); err != nil {
				t.Errorf("List: %v", err)
			}
		})
	}
	wg.Wait()
}

// TestStore_OptionsNodeIsOpaqueYamlNode pins that Profile.Options wraps a real
// *yaml.Node parsed from the `options:` key, so a builder can decode it.
func TestStore_OptionsNodeIsOpaqueYamlNode(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	st := New(dir)
	p, err := st.Get("default")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Options.Node() == nil {
		t.Fatal("Options.Node() must be non-nil after parsing options:")
	}
	if p.Options.Node().Kind != yaml.MappingNode {
		t.Fatalf("Options.Node().Kind = %d, want MappingNode", p.Options.Node().Kind)
	}
}

// --- Envelope form (apiVersion + spec, Phase A) ---

// envelopeYAML is a valid envelope-form docker profile: apiVersion + spec with
// cwd/shell inside spec.
const envelopeYAML = `apiVersion: docker.profiles.sandbox.teran.dev/v1
name: envdefault
description: the envelope default profile
spec:
  image: docker.io/library/alpine:3.20
  env:
    A: "1"
  network:
    mode: bridge
  user:
    uid: 1000
    gid: 1000
  sudo: never
  cwd:
    target: /work
    mode: rw
  shell: /bin/sh
`

// TestGet_EnvelopeParses pins that an envelope-form profile parses into the
// domain profile: driver from the apiVersion label, declared apiVersion,
// cwd/shell extracted from spec, and the remaining spec keys as opaque options
// (with cwd/shell stripped).
func TestGet_EnvelopeParses(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "env.yml", envelopeYAML)
	st := New(dir)

	p, err := st.Get("envdefault")
	if err != nil {
		t.Fatalf("Get(envdefault): %v", err)
	}
	if p.Driver != spec.DriverDocker {
		t.Fatalf("Driver = %q, want docker (from apiVersion label)", p.Driver)
	}
	if p.APIVersion != "docker.profiles.sandbox.teran.dev/v1" {
		t.Fatalf("APIVersion = %q, want docker.../v1", p.APIVersion)
	}
	if p.Cwd.Target != "/work" || p.Cwd.Mode != "rw" {
		t.Fatalf("Cwd = %+v, want target=/work mode=rw from spec", p.Cwd)
	}
	if p.Shell != "/bin/sh" {
		t.Fatalf("Shell = %q, want /bin/sh from spec", p.Shell)
	}
	// The driver options carry the remaining spec keys; cwd/shell are stripped.
	if p.Options.Node() == nil || p.Options.Node().Kind != yaml.MappingNode {
		t.Fatal("Options node must be a mapping")
	}
	var o struct {
		Image string `yaml:"image"`
		Cwd   string `yaml:"cwd"`
		Shell string `yaml:"shell"`
	}
	if err := p.Options.Node().Decode(&o); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if o.Image != "docker.io/library/alpine:3.20" {
		t.Fatalf("options image = %q", o.Image)
	}
	if o.Cwd != "" || o.Shell != "" {
		t.Fatalf("options must NOT carry cwd/shell, got cwd=%q shell=%q", o.Cwd, o.Shell)
	}
}

// TestGet_EnvelopeShellDefaultsBash pins that an envelope profile without a
// shell in spec defaults to bash.
func TestGet_EnvelopeShellDefaultsBash(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "env.yml", strings.Replace(envelopeYAML, "  shell: /bin/sh\n", "", 1))
	st := New(dir)
	p, err := st.Get("envdefault")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Shell != "bash" {
		t.Fatalf("Shell = %q, want default bash", p.Shell)
	}
}

// TestGet_EnvelopeSeatbelt pins an envelope seatbelt profile: driver seatbelt
// from the apiVersion, sbpl/env carried in the stripped options.
func TestGet_EnvelopeSeatbelt(t *testing.T) {
	const yaml = `apiVersion: seatbelt.profiles.sandbox.teran.dev/v1
name: envmac
description: an envelope seatbelt profile
spec:
  sbpl: |
    (allow default)
    (allow process-fork)
  env:
    MY_VAR: hello
  cwd:
    mode: ro
`
	dir := t.TempDir()
	writeProfile(t, dir, "envmac.yml", yaml)
	st := New(dir)
	p, err := st.Get("envmac")
	if err != nil {
		t.Fatalf("Get(envmac): %v", err)
	}
	if p.Driver != spec.DriverSeatbelt {
		t.Fatalf("Driver = %q, want seatbelt", p.Driver)
	}
	if p.APIVersion != "seatbelt.profiles.sandbox.teran.dev/v1" {
		t.Fatalf("APIVersion = %q", p.APIVersion)
	}
	var o struct {
		SBPL string            `yaml:"sbpl"`
		Env  map[string]string `yaml:"env"`
	}
	if err := p.Options.Node().Decode(&o); err != nil {
		t.Fatalf("decode options: %v", err)
	}
	if !strings.Contains(o.SBPL, "(allow process-fork)") || o.Env["MY_VAR"] != "hello" {
		t.Fatalf("options = %+v", o)
	}
}

// TestGet_EnvelopeUnknownWellFormedDriver pins that an envelope profile with a
// well-formed but unknown driver is accepted at scan (fails at USE time).
func TestGet_EnvelopeUnknownWellFormedDriver(t *testing.T) {
	const yaml = `apiVersion: bwrap.profiles.sandbox.teran.dev/v3
name: envbwrap
description: an unknown-driver envelope profile
spec:
  anything: here
`
	dir := t.TempDir()
	writeProfile(t, dir, "envbwrap.yml", yaml)
	st := New(dir)
	p, err := st.Get("envbwrap")
	if err != nil {
		t.Fatalf("Get(envbwrap): %v", err)
	}
	if p.Driver != spec.Driver("bwrap") {
		t.Fatalf("Driver = %q, want bwrap", p.Driver)
	}
	if p.APIVersion != "bwrap.profiles.sandbox.teran.dev/v3" {
		t.Fatalf("APIVersion = %q", p.APIVersion)
	}
}

// TestGet_EnvelopeMalformedAPIVersion pins a malformed apiVersion fails closed
// at scan (ErrInvalidAPIVersion wrapped).
func TestGet_EnvelopeMalformedAPIVersion(t *testing.T) {
	for _, apiVersion := range []string{"nope", "docker/v1", "docker.example.com/v1", ""} {
		dir := t.TempDir()
		writeProfile(t, dir, "bad.yml",
			"apiVersion: "+apiVersion+"\nname: badp\ndescription: bad apiVersion\nspec:\n  image: alpine\n")
		st := New(dir)
		if _, err := st.Get("badp"); err == nil {
			t.Errorf("apiVersion %q: expected scan error", apiVersion)
		}
	}
}

// TestGet_EnvelopeMixedWithDriver pins that apiVersion + top-level driver is
// rejected at scan (mixing the two forms).
func TestGet_EnvelopeMixedWithDriver(t *testing.T) {
	const yaml = `apiVersion: docker.profiles.sandbox.teran.dev/v1
name: mixed
description: mixed form
driver: docker
spec:
  image: alpine
`
	dir := t.TempDir()
	writeProfile(t, dir, "mixed.yml", yaml)
	st := New(dir)
	if _, err := st.Get("mixed"); err == nil {
		t.Fatal("expected error for apiVersion+driver mix")
	} else if !strings.Contains(err.Error(), "not both") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGet_EnvelopeMixedWithTopLevelCwdShell pins that apiVersion + top-level
// cwd/shell is rejected at scan.
func TestGet_EnvelopeMixedWithTopLevelCwdShell(t *testing.T) {
	const yaml = `apiVersion: docker.profiles.sandbox.teran.dev/v1
name: mixedcwd
description: mixed cwd form
spec:
  image: alpine
cwd:
  mode: rw
shell: bash
`
	dir := t.TempDir()
	writeProfile(t, dir, "mixedcwd.yml", yaml)
	st := New(dir)
	if _, err := st.Get("mixedcwd"); err == nil {
		t.Fatal("expected error for apiVersion+top-level cwd/shell")
	} else if !strings.Contains(err.Error(), "must live inside spec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGet_LegacyStillAccepted pins that the legacy driver+options form remains
// accepted (Phase A additive) — validYAML is the canonical legacy document.
func TestGet_LegacyStillAccepted(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "default.yml", validYAML)
	st := New(dir)
	p, err := st.Get("default")
	if err != nil {
		t.Fatalf("Get(default): %v", err)
	}
	if p.Driver != spec.DriverDocker || p.APIVersion != "" {
		t.Fatalf("Driver=%q APIVersion=%q, want docker/empty (legacy)", p.Driver, p.APIVersion)
	}
	if p.Cwd.Target != "/work" || p.Cwd.Mode != "ro" {
		t.Fatalf("Cwd = %+v, want legacy top-level cwd", p.Cwd)
	}
}

// TestGet_EnvelopeCwdExtractedFromSpec pins that cwd inside spec is extracted
// into Profile.Cwd (and stripped from options).
func TestGet_EnvelopeCwdExtractedFromSpec(t *testing.T) {
	const yaml = `apiVersion: docker.profiles.sandbox.teran.dev/v1
name: cwdp
description: cwd extraction
spec:
  image: alpine
  cwd:
    target: /src
    mode: rw
`
	dir := t.TempDir()
	writeProfile(t, dir, "cwdp.yml", yaml)
	st := New(dir)
	p, err := st.Get("cwdp")
	if err != nil {
		t.Fatalf("Get(cwdp): %v", err)
	}
	if p.Cwd.Target != "/src" || p.Cwd.Mode != "rw" {
		t.Fatalf("Cwd = %+v, want target=/src mode=rw", p.Cwd)
	}
}

// TestGet_EnvelopeSpecDecodeError pins that a spec whose cwd/shell common
// fields are not the expected shape fails closed with a decode error.
func TestGet_EnvelopeSpecDecodeError(t *testing.T) {
	const yaml = `apiVersion: docker.profiles.sandbox.teran.dev/v1
name: badspec
description: bad spec
spec:
  image: alpine
  cwd: not-a-mapping
`
	dir := t.TempDir()
	writeProfile(t, dir, "badspec.yml", yaml)
	st := New(dir)
	if _, err := st.Get("badspec"); err == nil {
		t.Fatal("expected error decoding a scalar cwd inside spec")
	} else if !strings.Contains(err.Error(), "decode spec cwd/shell") {
		t.Fatalf("unexpected error: %v", err)
	}
}
