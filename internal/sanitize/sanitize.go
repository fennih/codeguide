// Package sanitize turns untrusted text (prompts, tool input, model output)
// into a single printable line that cannot inject terminal escape sequences.
package sanitize

import (
	"regexp"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Line strips escape sequences and control characters, collapses whitespace,
// and truncates to maxWidth terminal cells (0 = no limit).
func Line(s string, maxWidth int) string {
	s = stripEscapes(s)
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			space = true
			continue
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			continue
		// Bidi overrides and isolates (Trojan Source) and zero-width joiners.
		case (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) || r == 0x200b || r == 0x200e || r == 0x200f || r == 0xfeff:
			continue
		case r == 0xfffd:
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if maxWidth > 0 {
		out = Truncate(out, maxWidth)
	}
	return out
}

// Truncate cuts an already clean string to maxWidth cells, adding an ellipsis.
func Truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	return runewidth.Truncate(s, maxWidth, "…")
}

// stripEscapes removes ESC-introduced sequences: CSI, OSC, DCS/SOS/PM/APC
// strings, and two-byte escapes. Unterminated strings are dropped to the end.
func stripEscapes(s string) string {
	if !strings.ContainsRune(s, 0x1b) && !strings.ContainsAny(s, "\u009b\u009d\u0090") {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b && i+1 < len(rs):
			next := rs[i+1]
			switch next {
			case '[':
				i = skipCSI(rs, i+2)
			case ']', 'P', 'X', '^', '_':
				i = skipString(rs, i+2)
			default:
				i++ // two-byte escape
			}
		case r == 0x1b:
			// trailing lone ESC
		case r == 0x9b:
			i = skipCSI(rs, i+1)
		case r == 0x9d || r == 0x90:
			i = skipString(rs, i+1)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// skipCSI returns the index of the CSI final byte (0x40-0x7e).
func skipCSI(rs []rune, i int) int {
	for ; i < len(rs); i++ {
		if rs[i] >= 0x40 && rs[i] <= 0x7e {
			return i
		}
	}
	return len(rs)
}

// skipString returns the index of the string terminator (BEL, ST, or ESC \).
func skipString(rs []rune, i int) int {
	for ; i < len(rs); i++ {
		switch rs[i] {
		case 0x07, 0x9c:
			return i
		case 0x1b:
			if i+1 < len(rs) && rs[i+1] == '\\' {
				return i + 1
			}
		}
	}
	return len(rs)
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	regexp.MustCompile(`(?i)\b(authorization|x-api-key|api[_-]?key|secret|token|password|passwd)(["']?\s*[:=]\s*["']?)(bearer\s+)?[^\s"',;]{6,}`),
}

// Redact masks common credential formats before text leaves the machine.
func Redact(s string) string {
	for i, re := range secretPatterns {
		if i == len(secretPatterns)-1 {
			s = re.ReplaceAllString(s, "$1$2[REDACTED]")
			continue
		}
		s = re.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
