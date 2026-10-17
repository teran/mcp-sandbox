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

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	domainprofile "github.com/teran/mcp-sandbox/domain/sandbox/profile"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// yamlProfile mirrors the operator-authored YAML document under the
// driver-registry schema (SPEC §7.11): a REQUIRED concrete `driver` plus an
// opaque `options:` blob, with `cwd`/`shell` on the top level. The store parses
// `options:` into an opaque yaml.Node without interpreting it — the concrete
// driver builder decodes it.
type yamlProfile struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Driver      string    `yaml:"driver"`
	Options     yaml.Node `yaml:"options"`
	Cwd         yamlCwd   `yaml:"cwd"`
	Shell       string    `yaml:"shell"`
}

type yamlCwd struct {
	Target string `yaml:"target"`
	Mode   string `yaml:"mode"`
}

// toDomain converts the parsed YAML into a domain Profile. The driver is kept
// as-authored (empty stays empty so Validate rejects it; there is no auto
// resolution at the profile layer). `options:` is wrapped as an opaque
// driver.Options; an absent `options:` key yields a nil node.
func (yp *yamlProfile) toDomain() *domainprofile.Profile {
	var opts driver.Options
	if yp.Options.Kind != 0 {
		opts = driver.NewOptions(&yp.Options)
	} else {
		opts = driver.NewOptions(nil)
	}

	p := &domainprofile.Profile{
		Name:        yp.Name,
		Description: yp.Description,
		Driver:      spec.Driver(yp.Driver),
		Cwd: domainprofile.CwdConfig{
			Target: yp.Cwd.Target,
			Mode:   yp.Cwd.Mode,
		},
		Shell:   yp.Shell,
		Options: opts,
	}
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
