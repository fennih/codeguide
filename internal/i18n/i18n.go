// Package i18n holds the interface strings. Code uses the English text as
// the key; other languages map it to a translation, falling back to English.
package i18n

import (
	"fmt"
	"strings"
)

var table map[string]string

// Set selects the interface language: "it" for Italian, anything else English.
func Set(lang string) {
	table = nil
	if lang == "it" {
		table = italian
	}
}

// T translates an English message and formats it with args.
func T(msg string, args ...any) string {
	if s, ok := table[msg]; ok {
		msg = s
	}
	// A singular form such as "1 turn" may drop the count in some languages.
	if len(args) == 0 || !strings.Contains(msg, "%") {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// N picks the singular or plural message for n and formats it with n.
func N(n int, one, many string) string {
	if n == 1 {
		return T(one, n)
	}
	return T(many, n)
}
