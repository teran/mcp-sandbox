// Package docker implements the built-in `docker` driver builder (Slices 2+3
// of the driver-registry + opaque options work, SPEC §6.11/§7.11). buildDocker
// decodes the opaque `options` yaml.Node carried in driver.Context into this
// package's typed docker options and produces a spec.SandboxSpec equivalent to
// the pre-migration profile.BuildSpec docker branch. It registers itself into
// the process-wide driver.Default registry via init().
package docker

import (
	"fmt"
	"strings"

	"github.com/distribution/reference"
	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// dockerOptions is the typed projection of the docker `options:` blob.
type dockerOptions struct {
	Image   string            `yaml:"image"`
	Env     map[string]string `yaml:"env"`
	Mounts  []dockerMount     `yaml:"mounts"`
	Network dockerNetwork     `yaml:"network"`
	Cgroups dockerCgroups     `yaml:"cgroups"`
	User    *spec.UIDGID      `yaml:"user"`
	Sudo    string            `yaml:"sudo"`
}

type dockerMount struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only"`
}

type dockerNetwork struct {
	Mode string `yaml:"mode"`
}

// dockerCgroups mirrors the cgroups block: either the raw values (nano_cpus /
// memory / memory_swap) or the friendly units (cpu_cores / memory_gb) may be
// used; raw values take precedence when both are present.
type dockerCgroups struct {
	NanoCPUs   int64         `yaml:"nano_cpus"`
	Memory     int64         `yaml:"memory"`
	MemorySwap int64         `yaml:"memory_swap"`
	PidsLimit  int64         `yaml:"pids_limit"`
	CPUCores   float64       `yaml:"cpu_cores"`
	MemoryGB   int64         `yaml:"memory_gb"`
	Ulimits    []spec.Ulimit `yaml:"ulimits"`
}

// allowedDockerKeys is the set of option keys the docker builder understands.
// Any other top-level key fails closed at build time (SPEC §7.11).
var allowedDockerKeys = map[string]struct{}{
	"image": {}, "env": {}, "mounts": {}, "network": {}, "cgroups": {}, "user": {}, "sudo": {},
}

// Conversion constants: CPU cores → nanoseconds and GiB → bytes.
const (
	nanoCPUsPerCore = 1_000_000_000
	bytesPerGiB     = 1024 * 1024 * 1024
)

// init registers the docker builder into the process-wide driver registry. The
// builder declares the schema apiVersion it supports, so the server can
// reconcile a profile's declared apiVersion against it (SPEC §7.11).
func init() {
	_ = driver.Register(spec.DriverDocker, driver.BuildFuncWithVersion(buildDocker, "docker.profiles.sandbox.teran.dev/v1"))
}

// checkKeys fail-closes on an unknown top-level option key.
func checkKeys(node *yaml.Node, allowed map[string]struct{}) error {
	node = rootNode(node)
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i]
		if key.Kind == yaml.ScalarNode {
			if _, ok := allowed[key.Value]; !ok {
				return fmt.Errorf("docker: unknown option key %q", key.Value)
			}
		}
	}

	return nil
}

// rootNode unwraps a yaml.Unmarshal-produced DocumentNode to its root mapping,
// so both a whole-document node (driver tests) and a bare mapping node (profile
// store) are handled identically.
func rootNode(node *yaml.Node) *yaml.Node {
	if node != nil && node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}

	return node
}

// buildDocker builds a docker SandboxSpec from the driver Context: it decodes
// the opaque options, applies friendly-unit cgroup conversion with raw
// precedence and MemorySwap defaulting to Memory, mounts the cwd first, and
// overrides the profile-owned resource/network fields while keeping the
// immutable hardening fields from the Policy.
func buildDocker(ctx driver.Context) (spec.SandboxSpec, error) {
	node := rootNode(ctx.Options.Node())
	var o dockerOptions
	if node != nil {
		if err := checkKeys(node, allowedDockerKeys); err != nil {
			return spec.SandboxSpec{}, err
		}
		if err := node.Decode(&o); err != nil {
			return spec.SandboxSpec{}, fmt.Errorf("docker: decode options: %w", err)
		}
	}
	// Image validation is parse-only: any valid reference from any registry is
	// accepted; only empty or malformed references are rejected.
	if _, err := reference.ParseNormalizedNamed(strings.TrimSpace(o.Image)); err != nil {
		return spec.SandboxSpec{}, fmt.Errorf("docker: invalid image %q: %w", o.Image, err)
	}

	nano, mem, swap, pids, ulims := resolveResources(o)

	// Immutable hardening comes from the Policy; only resource/network fields
	// are overridden by the profile-owned options.
	iso := ctx.Policy
	iso.NanoCPUs = nano
	iso.Memory = mem
	iso.MemorySwap = swap
	iso.PidsLimit = pids
	iso.Ulimits = ulims
	iso.NetworkMode = spec.NetworkMode(o.Network.Mode)

	// The enforced non-root user from the Policy is never weakened. A profile
	// may substitute a different non-root user; a nil or root profile user
	// falls back to the policy user.
	uidgid := ctx.Policy.User
	if o.User != nil && o.User.UID != 0 {
		uidgid = *o.User
	}

	cwdTarget := cwdTarget(ctx)
	mounts := []spec.Mount{{Source: ctx.Params.Cwd, Target: cwdTarget, ReadOnly: ctx.Cwd.Mode != "rw"}}
	for _, m := range o.Mounts {
		mounts = append(mounts, spec.Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}

	return spec.SandboxSpec{
		Driver:          spec.DriverDocker,
		Image:           o.Image,
		WorkingDir:      cwdTarget,
		Env:             o.Env,
		Mounts:          mounts,
		Shell:           defaultShell(ctx.Shell),
		Resources:       spec.Resources{NanoCPUs: nano, Memory: mem, MemorySwap: swap, PidsLimit: pids, Ulimits: ulims},
		Network:         spec.Network{Mode: iso.NetworkMode},
		UIDGID:          uidgid,
		Sudo:            spec.SudoPolicy(o.Sudo),
		Secrets:         ctx.Params.Secrets,
		ReadOnlyRoot:    true,
		Isolation:       iso,
		ClientSessionID: ctx.Params.ClientSessionID,
		Profile:         ctx.Name,
	}, nil
}

// resolveResources applies the friendly-unit cgroup conversion with raw-value
// precedence and MemorySwap defaulting to Memory (SPEC §7.11).
func resolveResources(o dockerOptions) (nano, mem, swap, pids int64, ulims []spec.Ulimit) {
	nano = o.Cgroups.NanoCPUs
	if nano == 0 && o.Cgroups.CPUCores > 0 {
		nano = int64(o.Cgroups.CPUCores * nanoCPUsPerCore)
	}
	mem = o.Cgroups.Memory
	if mem == 0 && o.Cgroups.MemoryGB > 0 {
		mem = o.Cgroups.MemoryGB * bytesPerGiB
	}
	swap = o.Cgroups.MemorySwap
	if mem > 0 && swap == 0 {
		swap = mem
	}
	pids = o.Cgroups.PidsLimit
	ulims = o.Cgroups.Ulimits

	return
}

// cwdTarget returns the container target for the session cwd mount: the
// profile-declared target, or the client path 1:1 when unset.
func cwdTarget(ctx driver.Context) string {
	if ctx.Cwd.Target != "" {
		return ctx.Cwd.Target
	}

	return ctx.Params.Cwd
}

// defaultShell returns the shell to build the sandbox_exec wrapper, defaulting
// to "bash" when unset (SPEC §7.10).
func defaultShell(shell string) string {
	if strings.TrimSpace(shell) == "" {
		return "bash"
	}

	return shell
}
