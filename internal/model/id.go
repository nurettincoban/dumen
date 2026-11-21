// Package model defines the records dumen stores and their validation rules.
package model

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
)

// Record ID prefixes.
const (
	PrefixDecision  = "D"
	PrefixDirective = "Y"
	PrefixRef       = "R"
	PrefixClaim     = "C"
)

// crockford is the Crockford base32 alphabet (no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var idRe = regexp.MustCompile(`^[DYRC]-[0-9A-HJKMNP-TV-Z]{4,8}$`)

// NewID returns prefix + "-" + 4 random Crockford base32 characters.
// IDs are never sequential; collisions are resolved when the operation is applied.
func NewID(prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dumen: crypto/rand failed: " + err.Error())
	}
	out := make([]byte, 4)
	for i, v := range b {
		out[i] = crockford[int(v)%len(crockford)] // 256 % 32 == 0, so no modulo bias
	}
	return prefix + "-" + string(out)
}

// ValidID reports whether id is a well-formed record ID with the given prefix
// (or any prefix when prefix is empty).
func ValidID(id, prefix string) bool {
	if !idRe.MatchString(id) {
		return false
	}
	return prefix == "" || strings.HasPrefix(id, prefix+"-")
}

// NewOpID returns a random 128-bit operation identifier in hex.
func NewOpID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("dumen: crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%x", b[:])
}

var slugStrip = regexp.MustCompile(`[^a-z0-9]+`)

// Slug converts free text into a short lowercase file-name fragment.
func Slug(s string) string {
	s = slugStrip.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}
