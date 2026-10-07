package sandbox

import (
	"regexp"
	"strings"
)

// ansiPattern matches ANSI escape sequences (CSI, OSC, and the 7-bit single
// escape). These are stripped from exec output to prevent terminal injection
// (S09/N23).
var ansiPattern = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// sanitizeANSI removes ANSI/control escape sequences from text output (S09).
// Only the printable characters plus tab, newline and carriage return are
// preserved; all other C0 control characters are dropped.
func sanitizeANSI(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	return sanitizeControl(s)
}

// sanitizeControl drops C0 control characters (0x00–0x1F) except tab, LF and
// CR.
func sanitizeControl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			continue
		}
		if r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
