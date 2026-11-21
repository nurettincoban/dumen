// Package secrets rejects text that looks like it contains a credential.
// It is a regex scan: it catches common token formats, not every possible secret.
package secrets

import (
	"fmt"
	"regexp"
)

type rule struct {
	name string
	re   *regexp.Regexp
}

var rules = []rule{
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"AWS access key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`)},
	{"GitLab token", regexp.MustCompile(`\bgl(pat|dt|rt|cbt|ptt|soat)-[A-Za-z0-9_\-]{20,}`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9\-]{10,}`)},
	{"Anthropic API key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"OpenAI API key", regexp.MustCompile(`\bsk-(proj-)?[A-Za-z0-9_\-]{32,}`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)},
	{"Stripe key", regexp.MustCompile(`\b[sr]k_(live|test)_[0-9A-Za-z]{20,}`)},
	{"JSON Web Token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
	{"credentials in URL", regexp.MustCompile(`[a-z][a-z0-9+.\-]*://[^/\s:@]+:[^/\s:@]{3,}@`)},
	{"assigned secret", regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_\-]?key|access[_\-]?token|auth[_\-]?token|private[_\-]?key)\b["']?\s*[:=]\s*["']?[^\s"']{8,}`)},
}

// Error reports which rule matched. It never includes the matched text.
type Error struct{ Rule string }

func (e *Error) Error() string {
	return fmt.Sprintf("rejected: text looks like it contains a secret (%s)", e.Rule)
}

// Scan returns an *Error if any of the texts matches a secret pattern.
func Scan(texts ...string) error {
	for _, t := range texts {
		if t == "" {
			continue
		}
		for _, r := range rules {
			if r.re.MatchString(t) {
				return &Error{Rule: r.name}
			}
		}
	}
	return nil
}
