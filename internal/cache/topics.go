package cache

import (
	"sort"
	"strings"
	"unicode"

	"github.com/nurettincoban/dumen/internal/model"
)

// model returns the parts of the full decision that its scope methods need.
func (d Decision) model() model.Decision {
	return model.Decision{AppliesTo: d.AppliesTo, Area: d.Area}
}

// ForService reports whether the decision applies to the service (see model.Decision).
func (d Decision) ForService(service string) bool { return d.model().ForService(service) }

// Topics returns the decision's topics, counting its area as one.
func (d Decision) Topics() []string { return d.model().Topics() }

// Headline is the rule, or the title of a decision without one.
func (d Decision) Headline() string {
	if d.Rule != "" {
		return d.Rule
	}
	return d.Title
}

// Match is an approved decision that a piece of text may be about.
type Match struct {
	Decision Decision
	Hits     []string // topic keywords found in the text
	// ThisService is true when the decision names the service the text came from, rather
	// than applying to the whole product.
	ThisService bool
}

// Strong reports a match good enough to hold a question back: two different keywords,
// or one keyword of a decision made for this very service.
func (m Match) Strong() bool {
	return len(m.Hits) >= 2 || (len(m.Hits) == 1 && m.ThisService)
}

// MatchText finds approved decisions whose topic keywords appear in text. A topic's own
// name counts as a keyword, so topics need not be declared in dumen.yml. Decisions
// limited to other services than service are left out when service is set. The result
// is ordered by the number of keywords hit, most first.
func (c *Cache) MatchText(text, service string) []Match {
	hay := normalize(text)
	var out []Match
	for _, d := range c.Decisions {
		if service != "" && !d.ForService(service) {
			continue
		}
		var hits []string
		for _, kw := range c.keywords(d) {
			if containsWord(hay, kw) && !has(hits, kw) {
				hits = append(hits, kw)
			}
		}
		if len(hits) == 0 {
			continue
		}
		this := service != "" && !d.model().ProductWide()
		out = append(out, Match{Decision: d, Hits: hits, ThisService: this})
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Hits) > len(out[j].Hits) })
	return out
}

func (c *Cache) keywords(d Decision) []string {
	var out []string
	for _, t := range d.Topics() {
		out = append(out, strings.TrimSpace(normalize(t)))
		for _, kw := range c.Config.Topics[t] {
			if kw = strings.TrimSpace(normalize(kw)); kw != "" {
				out = append(out, kw)
			}
		}
	}
	return out
}

// normalize lower-cases text and turns everything but letters and digits into single
// spaces, with a space at both ends, so that keywords match whole words.
func normalize(s string) string {
	var b strings.Builder
	b.WriteByte(' ')
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}

// containsWord reports whether the normalized keyword occurs in hay as whole words, also
// in a simple plural.
func containsWord(hay, kw string) bool {
	return kw != "" && (strings.Contains(hay, " "+kw+" ") || strings.Contains(hay, " "+kw+"s ") || strings.Contains(hay, " "+kw+"es "))
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
