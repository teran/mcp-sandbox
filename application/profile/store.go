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

// yamlProfile mirrors the operator-authored YAML document. Field names follow
// the profile schema (lowercased, snake_case for nested keys).
type yamlProfile struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Image       string            `yaml:"image"`
	Env         map[string]string `yaml:"env"`
	Mounts      []yamlMount       `yaml:"mounts"`
	Cwd         yamlCwd           `yaml:"cwd"`
	Network     yamlNetwork       `yaml:"network"`
	Cgroups     yamlCgroups       `yaml:"cgroups"`
	User        *yamlUser         `yaml:"user"`
	Sudo        string            `yaml:"sudo"`
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
// are present. CPU_cores is expressed in whole/virtual cores (1.0 = one core),
// Memory_GB in whole gibibytes.
type yamlCgroups struct {
	NanoCPUs   int64        `yaml:"nano_cpus"`
	Memory     int64        `yaml:"memory"`
	MemorySwap int64        `yaml:"memory_swap"`
	PidsLimit  int64        `yaml:"pids_limit"`
	CPU_cores  float64      `yaml:"cpu_cores"`
	Memory_GB  int64        `yaml:"memory_gb"`
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
func (yc yamlCgroups) toDomain() domainprofile.Cgroups {
	cg := domainprofile.Cgroups{
		NanoCPUs:   yc.NanoCPUs,
		Memory:     yc.Memory,
		MemorySwap: yc.MemorySwap,
		PidsLimit:  yc.PidsLimit,
	}
	if cg.NanoCPUs == 0 && yc.CPU_cores > 0 {
		cg.NanoCPUs = int64(yc.CPU_cores * 1_000_000_000)
	}
	if cg.Memory == 0 && yc.Memory_GB > 0 {
		cg.Memory = yc.Memory_GB * 1024 * 1024 * 1024
	}
	if cg.Memory > 0 && cg.MemorySwap == 0 {
		cg.MemorySwap = cg.Memory
	}
	return cg
}

// toDomain converts the parsed YAML into a domain Profile.
func (yp *yamlProfile) toDomain() *domainprofile.Profile {
	p := &domainprofile.Profile{
		Name:        yp.Name,
		Description: yp.Description,
		Image:       yp.Image,
		Env:         yp.Env,
		Cwd: domainprofile.CwdConfig{
			Target: yp.Cwd.Target,
			Mode:   yp.Cwd.Mode,
		},
		Network: domainprofile.Network{Mode: spec.NetworkMode(yp.Network.Mode)},
		Cgroups: yp.Cgroups.toDomain(),
		Sudo:    spec.SudoPolicy(yp.Sudo),
	}
	for _, m := range yp.Mounts {
		p.Mounts = append(p.Mounts, domainprofile.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	for _, u := range yp.Cgroups.Ulimits {
		p.Cgroups.Ulimits = append(p.Cgroups.Ulimits, spec.Ulimit{Name: u.Name, Soft: u.Soft, Hard: u.Hard})
	}
	if yp.User != nil {
		p.User = &spec.UIDGID{UID: yp.User.UID, GID: yp.User.GID}
	}
	return p
}

// Store is an immutable, in-memory view of the operator-authored profiles,
// keyed by profile name. It is read-only by construction.
type Store struct {
	profiles map[string]*domainprofile.Profile
}

// Load scans the profiles directory for *.yml / *.yaml files, parses and
// validates each and returns a Store. An empty or missing directory yields an
// empty store. A duplicate profile name, an invalid profile, or unparsable YAML
// is an error. The store is keyed by the validated profile name, which is
// filesystem-safe (name regex), so a hostile file cannot escape the directory.
func Load(dir string) (*Store, error) {
	st := &Store{profiles: make(map[string]*domainprofile.Profile)}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, fmt.Errorf("profile: read dir %q: %w", dir, err)
	}

	// Open the profiles directory once as a scoped root so that every file
	// read below is structurally confined to dir — traversal out of it is
	// impossible regardless of fname (which comes from ReadDir and is a bare
	// base name, but os.Root enforces the boundary even against hostile input).
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("profile: open root %q: %w", dir, err)
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
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("profile: %s: %w", fname, err)
		}
		if _, exists := st.profiles[p.Name]; exists {
			return nil, fmt.Errorf("profile: duplicate name %q", p.Name)
		}
		st.profiles[p.Name] = p
	}

	return st, nil
}

// Empty returns a functional empty store: Get always reports not-found and
// List returns an empty slice. It is used to keep the server operational with a
// non-nil store when profile loading fails (an operator mistake must not take
// profile_list / sandbox_create down entirely).
func Empty() *Store {
	return &Store{profiles: make(map[string]*domainprofile.Profile)}
}

// Get returns the profile with the given name.
func (s *Store) Get(name string) (*domainprofile.Profile, bool) {
	p, ok := s.profiles[name]
	return p, ok
}

// List returns every profile sorted by name.
func (s *Store) List() []*domainprofile.Profile {
	names := make([]string, 0, len(s.profiles))
	for n := range s.profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*domainprofile.Profile, 0, len(names))
	for _, n := range names {
		out = append(out, s.profiles[n])
	}
	return out
}
