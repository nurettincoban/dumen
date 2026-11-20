// Package render produces the text dumen shows to agents and the generated Markdown files.
package render

import (
	"strings"
	"unicode"
)

// MaxLabel is the longest label injected into an agent's context.
const MaxLabel = 120

// Label makes untrusted record text safe to inject as a one-line label: control,
// format and other invisible characters are removed, whitespace is collapsed, and the
// result is cut to max characters.
func Label(s string, max int) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r),
			unicode.Is(unicode.Cs, r), unicode.Is(unicode.Zl, r), unicode.Is(unicode.Zp, r),
			unicode.Is(unicode.Mn, r) && r >= 0xFE00 && r <= 0xFE0F, // variation selectors
			r >= 0xE0000 && r <= 0xE0FFF,                            // tag characters
			r == unicode.ReplacementChar:
			continue
		}
		if space {
			if n+1 >= max {
				break
			}
			b.WriteByte(' ')
			n++
			space = false
		}
		if n >= max {
			return strings.TrimSpace(b.String()[:b.Len()]) + "…"
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
