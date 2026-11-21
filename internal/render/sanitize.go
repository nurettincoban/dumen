// Package render produces the text dumen shows to agents and the generated Markdown files.
package render

import (
	"strings"
	"unicode"

	"github.com/nurettincoban/dumen/internal/model"
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

// Summary turns an assistant message into a one-line activity summary. The activity log
// must never carry code, so fenced code blocks are dropped whole, only the first
// paragraph of prose is kept, and Markdown emphasis and heading marks are removed.
func Summary(s string, max int) string {
	var prose []string
	inFence := false
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t") {
			continue // fenced or indented code
		}
		if trimmed == "" {
			if len(prose) > 0 {
				break // end of the first paragraph
			}
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#>")
		trimmed = strings.TrimSpace(strings.NewReplacer("**", "", "__", "").Replace(trimmed))
		if trimmed != "" {
			prose = append(prose, trimmed)
		}
	}
	return Label(shortenCode(strings.Join(prose, " ")), max)
}

// shortenCode keeps short inline code spans (names, paths, commands) and replaces long
// ones, which are more likely to be source code, with an ellipsis.
func shortenCode(s string) string {
	const longest = 40
	var b strings.Builder
	for {
		open := strings.IndexByte(s, '`')
		if open < 0 {
			break
		}
		end := strings.IndexByte(s[open+1:], '`')
		if end < 0 {
			break
		}
		span := s[open+1 : open+1+end]
		b.WriteString(s[:open])
		if len(span) > longest {
			b.WriteString("`…`")
		} else {
			b.WriteString("`" + span + "`")
		}
		s = s[open+end+2:]
	}
	b.WriteString(s)
	return b.String()
}

// Until formats an expiry time the same way everywhere a person or an agent sees it: in
// the machine's local time zone, to the minute.
func Until(ts string) string {
	t, err := model.ParseTime(ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("2006-01-02 15:04")
}
