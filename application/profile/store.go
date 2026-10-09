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

type yamlCgroups struct {
	NanoCPUs   int64        `yaml:"nano_cpus"`
	Memory     int64        `yaml:"memory"`
	MemorySwap int64        `yaml:"memory_swap"`
	PidsLimit  int64        `yaml:"pids_limit"`
	Ulimits    []yamlUlimit `yaml:"ulimits"`
}

type yamlUser struct {
	UID uint32 `yaml:"uid"`
	GID uint32 `yaml:"gid"`
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
		Cgroups: domainprofile.Cgroups{
			NanoCPUs:   yp.Cgroups.NanoCPUs,
			Memory:     yp.Cgroups.Memory,
			MemorySwap: yp.Cgroups.MemorySwap,
			PidsLimit:  yp.Cgroups.PidsLimit,
		},
		Sudo: spec.SudoPolicy(yp.Sudo),
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
	defer root.Close()

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
		f.Close()
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
