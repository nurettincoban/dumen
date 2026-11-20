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

func TestSummaryCarriesNoCode(t *testing.T) {
	msg := "## Done\n\nI added **`getOrder`** to `src/api/orders.ts`,\nbut not `logout`.\n\n```ts\nexport const secretSauce = 42;\n```\n\nMore detail here."
	got := Summary(msg, 200)
	if got != "Done" {
		t.Fatalf("heading paragraph: %q", got)
	}
	msg = "I added `getOrder` to `src/api/orders.ts`,\nbut not `logout`:\n```ts\nexport const secretSauce = 42;\n```\n    indented := code()\n\nSecond paragraph."
	got = Summary(msg, 200)
	if got != "I added `getOrder` to `src/api/orders.ts`, but not `logout`:" {
		t.Fatalf("summary: %q", got)
	}
	long := "Changed `const handler = async (req, res) => { return res.json(await db.query(sql)) }` today."
	if got = Summary(long, 200); got != "Changed `…` today." {
		t.Fatalf("long inline code: %q", got)
	}
}
