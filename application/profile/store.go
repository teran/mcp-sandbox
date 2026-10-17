// Package profile implements the application-layer, read-only profile store
// (Phase 7). It scans an operator-authored profiles directory, parses each YAML
// file, validates it and exposes Get/List to the tools. There are no
// create/update/delete operations — the store is read-only by construction.
package profile

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// yamlProfile mirrors the operator-authored YAML document under the hybrid
// driver-aware schema (SPEC §7.11/§7.12): a `driver` plus a single
// `docker`/`seatbelt` block, with `cwd`/`shell` on the top level.
type yamlProfile struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Driver      string        `yaml:"driver"`
	Docker      *yamlDocker   `yaml:"docker"`
	Seatbelt    *yamlSeatbelt `yaml:"seatbelt"`
	Cwd         yamlCwd       `yaml:"cwd"`
	Shell       string        `yaml:"shell"`
}

// yamlDocker is the Docker-backend block of a profile (SPEC §7.11).
type yamlDocker struct {
	Image   string            `yaml:"image"`
	Env     map[string]string `yaml:"env"`
	Mounts  []yamlMount       `yaml:"mounts"`
	Network yamlNetwork       `yaml:"network"`
	Cgroups yamlCgroups       `yaml:"cgroups"`
	User    *yamlUser         `yaml:"user"`
	Sudo    string            `yaml:"sudo"`
}

// yamlSeatbelt is the darwin/Seatbelt block of a profile (SPEC §7.12).
type yamlSeatbelt struct {
	SBPL        string            `yaml:"sbpl"`
	MachLookups []string          `yaml:"mach_lookups"`
	Sysctls     []string          `yaml:"sysctls"`
	Env         map[string]string `yaml:"env"`
}

type yamlMount struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
}

type yamlCwd struct {
	Target string `yaml:"target"`
	Mode   string `yaml:"mode"`
}

type yamlNetwork struct {
	Mode string `yaml:"mode"`
}

type yamlUlimit struct {
	Name string `yaml:"name"`
	Soft uint64 `yaml:"soft"`
	Hard uint64 `yaml:"hard"`
}

// yamlCgroups is the cgroups block of an operator-authored profile. Either the
// raw values (nano_cpus / memory / memory_swap) or the friendly units
// (cpu_cores / memory_gb) may be used; the raw values take precedence when both
// are present. cpu_cores is expressed in whole/virtual cores (1.0 = one core),
// memory_gb in whole gibibytes.
type yamlCgroups struct {
	NanoCPUs   int64        `yaml:"nano_cpus"`
	Memory     int64        `yaml:"memory"`
	MemorySwap int64        `yaml:"memory_swap"`
	PidsLimit  int64        `yaml:"pids_limit"`
	CPUCores   float64      `yaml:"cpu_cores"`
	MemoryGB   int64        `yaml:"memory_gb"`
	Ulimits    []yamlUlimit `yaml:"ulimits"`
}

type yamlUser struct {
	UID uint32 `yaml:"uid"`
	GID uint32 `yaml:"gid"`
}

// toDomain converts the parsed cgroups block into the domain Cgroups, applying
// friendly-unit translation (SPEC §7.11). Rules:
//
//   - raw values take precedence; friendly units are only consulted when the
//     corresponding raw value is unset (0);
//   - cpu_cores -> NanoCPUs (cores * 1e9 nanos);
//   - memory_gb -> Memory (GiB);
//   - when only Memory is set, MemorySwap defaults to Memory so the
//     "memory_swap >= memory" Validate invariant holds with swap disabled.
// yamlCgroups fields mirror the YAML cgroups block; the _toDomain converter
// below turns them into domain Cgroups with the server-enforced defaults.

// Conversion constants: CPU cores → nanoseconds and GiB → bytes.
const (
	nanoCPUsPerCore = 1_000_000_000
	bytesPerGiB     = 1024 * 1024 * 1024
)

func (yc yamlCgroups) toDomain() domainprofile.Cgroups {
	cg := domainprofile.Cgroups{
		NanoCPUs:   yc.NanoCPUs,
		Memory:     yc.Memory,
		MemorySwap: yc.MemorySwap,
		PidsLimit:  yc.PidsLimit,
	}
	if cg.NanoCPUs == 0 && yc.CPUCores > 0 {
		cg.NanoCPUs = int64(yc.CPUCores * nanoCPUsPerCore)
	}
	if cg.Memory == 0 && yc.MemoryGB > 0 {
		cg.Memory = yc.MemoryGB * bytesPerGiB
	}
	if cg.Memory > 0 && cg.MemorySwap == 0 {
		cg.MemorySwap = cg.Memory
	}

	return cg
}

// toDomain converts the parsed YAML into a domain Profile.
func (yp *yamlProfile) toDomain() *domainprofile.Profile {
	driver := spec.Driver(yp.Driver)
	if driver == "" {
		driver = spec.DriverAuto
	}

	p := &domainprofile.Profile{
		Name:        yp.Name,
		Description: yp.Description,
		Driver:      driver,
		Cwd: domainprofile.CwdConfig{
			Target: yp.Cwd.Target,
			Mode:   yp.Cwd.Mode,
		},
	}
	if yp.Docker != nil {
		d := &domainprofile.DockerProfile{
			Image:   yp.Docker.Image,
			Env:     yp.Docker.Env,
			Network: domainprofile.Network{Mode: spec.NetworkMode(yp.Docker.Network.Mode)},
			Cgroups: yp.Docker.Cgroups.toDomain(),
			Sudo:    spec.SudoPolicy(yp.Docker.Sudo),
		}
		for _, m := range yp.Docker.Mounts {
			d.Mounts = append(d.Mounts, domainprofile.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
		}
		for _, u := range yp.Docker.Cgroups.Ulimits {
			d.Cgroups.Ulimits = append(d.Cgroups.Ulimits, spec.Ulimit{Name: u.Name, Soft: u.Soft, Hard: u.Hard})
		}
		if yp.Docker.User != nil {
			d.User = &spec.UIDGID{UID: yp.Docker.User.UID, GID: yp.Docker.User.GID}
		}
		p.Docker = d
	}
	if yp.Seatbelt != nil {
		p.Seatbelt = &domainprofile.SeatbeltProfile{
			SBPL:        yp.Seatbelt.SBPL,
			MachLookups: yp.Seatbelt.MachLookups,
			Sysctls:     yp.Seatbelt.Sysctls,
			Env:         yp.Seatbelt.Env,
		}
	}
	p.Shell = yp.Shell
	if strings.TrimSpace(p.Shell) == "" {
		p.Shell = "bash"
	}

	return p
}

// Store is a read-on-demand view of the operator-authored profiles directory.
// It holds only the directory path and keeps NO in-memory state: every Get/List
// call scans + parses + validates the directory fresh (no startup preload, no
// shared mutable state, hence no mutex — X05). It is read-only by construction.
type Store struct {
	dir string
}

// New returns a read-on-demand profile store rooted at dir.
func New(dir string) *Store {
	return &Store{dir: dir}
}

// scan reads, parses and validates every *.yml / *.yaml profile in the
// directory fresh and returns the keyed result. An empty or missing directory
// yields an empty map with a nil error; a duplicate name, an invalid profile,
// an unparsable YAML or a traversal declared name is a wrapped error
// (fail-closed, S04). Every file read is confined to dir via os.OpenRoot, so a
// hostile file cannot escape regardless of the file name.
func (s *Store) scan() (map[string]*domainprofile.Profile, error) {
	profiles := make(map[string]*domainprofile.Profile)

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return profiles, nil
		}

		return nil, fmt.Errorf("profile: read dir %q: %w", s.dir, err)
	}

	// Open the profiles directory once as a scoped root so that every file
	// read below is structurally confined to dir.
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, fmt.Errorf("profile: open root %q: %w", s.dir, err)
	}
	defer func() { _ = root.Close() }()

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if !strings.HasSuffix(fname, ".yml") && !strings.HasSuffix(fname, ".yaml") {
			continue
		}
		f, err := root.Open(fname)
		if err != nil {
			return nil, fmt.Errorf("profile: open %q: %w", fname, err)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("profile: read %q: %w", fname, err)
		}
		var yp yamlProfile
		if err := yaml.Unmarshal(data, &yp); err != nil {
			return nil, fmt.Errorf("profile: %s: parse: %w", fname, err)
		}
		p := yp.toDomain()
		// Reject a declared name that is invalid / tries to traverse out of the
		// profiles directory (fail-closed) before it could be used as a key.
		if !domainprofile.ValidName(p.Name) {
			return nil, fmt.Errorf("profile: %s: invalid name %q", fname, p.Name)
		}
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("profile: %s: %w", fname, err)
		}
		if _, exists := profiles[p.Name]; exists {
			return nil, fmt.Errorf("profile: duplicate name %q", p.Name)
		}
		profiles[p.Name] = p
	}

	return profiles, nil
}

// Get returns the profile with the given name. The lookup key is guarded
// up-front (an invalid key returns ErrInvalidName without ever being used as a
// path) and is then matched against the declared `name` of every scanned
// profile — never against a file path. An unknown name or a missing/empty
// directory returns ErrProfileNotFound; any scan error is propagated verbatim
// (fail-closed).
func (s *Store) Get(name string) (*domainprofile.Profile, error) {
	if !domainprofile.ValidName(name) {
		return nil, domainprofile.ErrInvalidName
	}
	profiles, err := s.scan()
	if err != nil {
		return nil, err
	}
	p, ok := profiles[name]
	if !ok {
		return nil, domainprofile.ErrProfileNotFound
	}

	return p, nil
}

// List returns every profile sorted by name, parsed fresh from the directory.
// An empty or missing directory yields an empty slice with a nil error; any
// scan error is propagated verbatim (fail-closed).
func (s *Store) List() ([]*domainprofile.Profile, error) {
	profiles, err := s.scan()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(profiles))
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*domainprofile.Profile, 0, len(names))
	for _, n := range names {
		out = append(out, profiles[n])
	}

	return out, nil
}
