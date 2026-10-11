// Package guidance provides the self-describing MCP guidance for mcp-sandbox:
// server-level Instructions text plus a registry of `sandbox://guidance/*`
// resources (overview, redirect, create, exec, filesystem-mounts, security)
// served to the model-host over the MCP resources protocol.
//
// The package MUST NOT import the go-sdk: it is pure application-layer data
// (markdown via //go:embed), so infrastructure can wire it without a layer edge
// violation. Content is secrets-safe (S02) and ANSI-safe (S09).
package guidance

import "embed"

// RedirectNote is the per-tool note appended to sandbox_create/sandbox_exec
// instructions, pointing the model at the shell-restricted-redirect resource
// and away from the host shell.
const RedirectNote = "The host shell is restricted — route commands through this sandbox server."

// Resource URIs (target contract).
const (
	URIOverview = "sandbox://guidance/overview"
	URIRedirect = "sandbox://guidance/shell-restricted-redirect"
	URICreate   = "sandbox://guidance/sandbox-create"
	URIExec     = "sandbox://guidance/sandbox-exec"
	URIFS       = "sandbox://guidance/filesystem-mounts"
	URISecurity = "sandbox://guidance/security"
)

// MIMEType is the content type of every guidance resource.
const MIMEType = "text/markdown"

// Resource is one self-describing guidance document.
type Resource struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	Content     string
}

//go:embed *.md
var docs embed.FS

// fileFor maps a guidance resource URI to the embedded markdown file name.
// The embed FS keys are file paths (e.g. "overview.md"), while resources are
// addressed by their sandbox://guidance/ URIs, so we translate between them.
func fileFor(uri string) (string, bool) {
	files := map[string]string{
		URIOverview: "overview.md",
		URIRedirect: "shell-restricted-redirect.md",
		URICreate:   "sandbox-create.md",
		URIExec:     "sandbox-exec.md",
		URIFS:       "filesystem-mounts.md",
		URISecurity: "security.md",
	}
	f, ok := files[uri]

	return f, ok
}

// registry is the deterministic-order resource index. It stays stable across
// calls so the model-host sees a consistent self-description.
var registry = []Resource{
	{
		URI:         URIOverview,
		Name:        "guidance-overview",
		Title:       "mcp-sandbox overview",
		Description: "Tool grouping, annotations, and human-in-the-loop for the mcp-sandbox server.",
		MIMEType:    MIMEType,
	},
	{
		URI:         URIRedirect,
		Name:        "guidance-shell-restricted-redirect",
		Title:       "Host shell restricted",
		Description: "Why the host shell is blocked and how to route commands through the sandbox instead.",
		MIMEType:    MIMEType,
	},
	{
		URI:         URICreate,
		Name:        "guidance-sandbox-create",
		Title:       "Creating a sandbox",
		Description: "How sandbox_create provisions an isolated container from an operator-authored profile.",
		MIMEType:    MIMEType,
	},
	{
		URI:         URIExec,
		Name:        "guidance-sandbox-exec",
		Title:       "Executing commands in a sandbox",
		Description: "How sandbox_exec runs commands via the fixed shell wrapper, env, timeout and streaming.",
		MIMEType:    MIMEType,
	},
	{
		URI:         URIFS,
		Name:        "guidance-filesystem-mounts",
		Title:       "Filesystem layout and mounts",
		Description: "Read-only rootfs, tmpfs /tmp, writable cwd, and the Go caches recipe.",
		MIMEType:    MIMEType,
	},
	{
		URI:         URISecurity,
		Name:        "guidance-security",
		Title:       "Security model",
		Description: "Immutable Isolation, secrets handling, HITL and output hygiene.",
		MIMEType:    MIMEType,
	},
}

// Instructions returns the server-level instructions text (redirect invariant +
// resource index).
func Instructions() string {
	return RedirectNote + "\n\n" +
		"Tools are grouped read → write → delete: sandbox_create provisions a container, " +
		"then sandbox_exec runs commands inside it; sandbox_list/sandbox_info/sandbox_stats " +
		"inspect; sandbox_destroy and sandbox_destroy_all remove sessions and require human " +
		"confirmation. Never place secrets in a command or env — deliver them as tmpfs 0600 " +
		"secret files on sandbox_create.\n\n" +
		"Self-describing resources:\n" +
		"- " + URIOverview + " — overview of tools, annotations and HITL\n" +
		"- " + URIRedirect + " — host-shell-restricted redirect workflow\n" +
		"- " + URICreate + " — creating a sandbox\n" +
		"- " + URIExec + " — executing commands\n" +
		"- " + URIFS + " — filesystem mounts and Go caches recipe\n" +
		"- " + URISecurity + " — security model"
}

// All returns the deterministic-order resource registry.
func All() []Resource {
	out := make([]Resource, len(registry))
	for i, r := range registry {
		out[i] = r
		if f, ok := fileFor(r.URI); ok {
			out[i].Content = readFile(f)
		}
	}

	return out
}

// Content returns the markdown body for a resource URI.
func Content(uri string) (string, bool) {
	f, ok := fileFor(uri)
	if !ok {
		return "", false
	}

	return readFile(f), true
}

// readFile reads an embedded markdown file; embedded files always exist, so a
// read failure (impossible in practice) yields the empty string.
func readFile(name string) string {
	b, err := docs.ReadFile(name)
	if err != nil {
		return ""
	}

	return string(b)
}

// URIs returns the registry index of resource URIs.
func URIs() []string {
	out := make([]string, len(registry))
	for i, r := range registry {
		out[i] = r.URI
	}

	return out
}
