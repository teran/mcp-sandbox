// Package darwin implements the built-in `seatbelt` driver builder (Slices 2+3
// of the driver-registry + opaque options work, SPEC §6.11/§7.12). buildSeatbelt
// decodes the opaque `options` yaml.Node carried in driver.Context into this
// package's typed seatbelt options and produces a spec.SandboxSpec equivalent
// to the pre-migration profile.BuildSpec seatbelt branch. It registers itself
// into the process-wide driver.Default registry via init().
package darwin

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/teran/mcp-sandbox/domain/sandbox/driver"
	"github.com/teran/mcp-sandbox/domain/sandbox/spec"
)

// seatbeltOptions is the typed projection of the seatbelt `options:` blob.
type seatbeltOptions struct {
	SBPL        string            `yaml:"sbpl"`
	MachLookups []string          `yaml:"mach_lookups"`
	Sysctls     []string          `yaml:"sysctls"`
	Env         map[string]string `yaml:"env"`
}

// allowedSeatbeltKeys is the set of option keys the seatbelt builder
// understands. Any other top-level key fails closed at build time.
var allowedSeatbeltKeys = map[string]struct{}{
	"sbpl": {}, "mach_lookups": {}, "sysctls": {}, "env": {},
}

// init registers the seatbelt builder into the process-wide driver registry.
// The builder declares the schema apiVersion it supports, so the server can
// reconcile a profile's declared apiVersion against it (SPEC §7.11).
func init() {
	_ = driver.Register(spec.DriverSeatbelt, driver.BuildFuncWithVersion(buildSeatbelt, "seatbelt.profiles.sandbox.teran.dev/v1"))
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
				return fmt.Errorf("darwin: unknown option key %q", key.Value)
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

// buildSeatbelt builds a darwin/Seatbelt SandboxSpec from the driver Context:
// no image, the Sandbox block and env from the opaque options, Sudo=SudoNever,
// UIDGID/Resources/Network from the immutable Policy, only the cwd mount, and
// hardening from the Policy.
func buildSeatbelt(ctx driver.Context) (spec.SandboxSpec, error) {
	node := rootNode(ctx.Options.Node())
	var o seatbeltOptions
	if node != nil {
		if err := checkKeys(node, allowedSeatbeltKeys); err != nil {
			return spec.SandboxSpec{}, err
		}
		if err := node.Decode(&o); err != nil {
			return spec.SandboxSpec{}, fmt.Errorf("darwin: decode options: %w", err)
		}
	}

	pol := ctx.Policy
	cwdTarget := cwdTarget(ctx)

	return spec.SandboxSpec{
		Driver:     spec.DriverSeatbelt,
		WorkingDir: cwdTarget,
		Env:        o.Env,
		Mounts:     []spec.Mount{{Source: ctx.Params.Cwd, Target: cwdTarget, ReadOnly: ctx.Cwd.Mode != "rw"}},
		Shell:      shellOrDefault(ctx.Shell),
		Resources: spec.Resources{
			NanoCPUs:   pol.NanoCPUs,
			Memory:     pol.Memory,
			MemorySwap: pol.MemorySwap,
			PidsLimit:  pol.PidsLimit,
			Ulimits:    pol.Ulimits,
		},
		Network:         spec.Network{Mode: pol.NetworkMode},
		UIDGID:          pol.User,
		Sudo:            spec.SudoNever,
		Secrets:         ctx.Params.Secrets,
		ReadOnlyRoot:    true,
		Isolation:       pol,
		Sandbox:         spec.Sandbox{SBPL: o.SBPL, MachLookups: o.MachLookups, Sysctls: o.Sysctls},
		ClientSessionID: ctx.Params.ClientSessionID,
	}, nil
}

// cwdTarget returns the container target for the session cwd mount: the
// profile-declared target, or the client path 1:1 when unset.
func cwdTarget(ctx driver.Context) string {
	if ctx.Cwd.Target != "" {
		return ctx.Cwd.Target
	}

	return ctx.Params.Cwd
}

// shellOrDefault returns the shell to build the sandbox_exec wrapper,
// defaulting to "bash" when unset (SPEC §7.10). It is named to avoid colliding
// with the package-level `defaultShell` constant.
func shellOrDefault(shell string) string {
	if strings.TrimSpace(shell) == "" {
		return "bash"
	}

	return shell
}
