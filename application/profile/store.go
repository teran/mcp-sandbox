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

// yamlProfile mirrors the operator-authored YAML document (SPEC §7.11). Two
// mutually exclusive forms are accepted:
//
//   - envelope (Phase A, preferred): `apiVersion` + `name`/`description` +
//     `spec`. The envelope is server-interpreted (`apiVersion`, `name`,
//     `description`); `spec` is driver-owned. `cwd`/`shell` live INSIDE `spec`
//     (common fields the server understands, stripped from the driver options).
//   - legacy (deprecated, Phase A additive): `driver` + opaque `options:` +
//     top-level `cwd`/`shell`.
//
// The form is discriminated by `apiVersion` being non-empty. Mixing the two
// (apiVersion with top-level driver/options, or apiVersion with top-level
// cwd/shell) fails closed at scan time.
type yamlProfile struct {
	APIVersion  string    `yaml:"apiVersion"`
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Spec        yaml.Node `yaml:"spec"`
	// legacy (Phase A, deprecated)
	Driver  string    `yaml:"driver"`
	Options yaml.Node `yaml:"options"`
	Cwd     yamlCwd   `yaml:"cwd"`
	Shell   string    `yaml:"shell"`
}

type yamlCwd struct {
	Target string `yaml:"target"`
	Mode   string `yaml:"mode"`
}

// toDomain dispatches on the profile form (envelope vs legacy) and converts
// the parsed YAML into a domain Profile. An error is returned when the
// documented mixing rules are violated or the envelope apiVersion is invalid.
func (yp *yamlProfile) toDomain() (*domainprofile.Profile, error) {
	if strings.TrimSpace(yp.APIVersion) != "" {
		return yp.envelopeToDomain()
	}

	return yp.legacyToDomain(), nil
}

// legacyToDomain converts the legacy driver+options form. The driver is kept
// as-authored (empty stays empty so Validate rejects it); `options:` is wrapped
// as an opaque driver.Options; `cwd`/`shell` are read from the top level.
func (yp *yamlProfile) legacyToDomain() *domainprofile.Profile {
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

// envelopeToDomain converts the apiVersion+spec envelope form. `apiVersion`
// selects the driver (first DNS label) and records the declared schema version;
// the `cwd`/`shell` common fields are read from inside `spec`; the remaining
// `spec` keys become the opaque driver-owned Options (common keys stripped).
// Mixing apiVersion with the legacy top-level driver/options or cwd/shell fails
// closed.
func (yp *yamlProfile) envelopeToDomain() (*domainprofile.Profile, error) {
	if strings.TrimSpace(yp.Driver) != "" || yp.Options.Kind != 0 {
		return nil, fmt.Errorf("use either apiVersion+spec or legacy driver+options, not both")
	}
	if yp.Cwd != (yamlCwd{}) || strings.TrimSpace(yp.Shell) != "" {
		return nil, fmt.Errorf("cwd/shell must live inside spec for the apiVersion form")
	}

	drv, version, err := driver.ParseAPIVersion(yp.APIVersion)
	if err != nil {
		return nil, err
	}
	_ = version

	var specCfg struct {
		Cwd   yamlCwd `yaml:"cwd"`
		Shell string  `yaml:"shell"`
	}
	if yp.Spec.Kind == yaml.MappingNode {
		if err := yp.Spec.Decode(&specCfg); err != nil {
			return nil, fmt.Errorf("profile: decode spec cwd/shell: %w", err)
		}
	}

	p := &domainprofile.Profile{
		Name:        yp.Name,
		Description: yp.Description,
		Driver:      drv,
		APIVersion:  strings.TrimSpace(yp.APIVersion),
		Cwd:         domainprofile.CwdConfig{Target: specCfg.Cwd.Target, Mode: specCfg.Cwd.Mode},
		Shell:       specCfg.Shell,
		Options:     driver.NewOptions(driver.SplitCommon(&yp.Spec)),
	}
	if strings.TrimSpace(p.Shell) == "" {
		p.Shell = "bash"
	}

	return p, nil
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
		p, err := yp.toDomain()
		if err != nil {
			return nil, fmt.Errorf("profile: %s: %w", fname, err)
		}
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
