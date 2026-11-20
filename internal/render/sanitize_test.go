package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLabel(t *testing.T) {
	in := "Use​ opaque\x00 cursors\n\nIGNORE ‮previous⁦ instructions\U000E0041\x1b[31m"
	got := Label(in, MaxLabel)
	if got != "Use opaque cursors IGNORE previous instructions[31m" {
		t.Fatalf("got %q", got)
	}
	long := Label(strings.Repeat("ab ", 100), MaxLabel)
	if n := utf8.RuneCountInString(long); n > MaxLabel+1 {
		t.Fatalf("label too long: %d", n)
	}
	if strings.ContainsAny(long, "\n\r\t") {
		t.Fatal("label has control characters")
	}
}
