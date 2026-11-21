package cli

import (
	"io"
	"os"
	"testing"
)

// The tests run without a terminal and stand in for a person at one; the test of the
// terminal rule switches this off.
var realIsTerminal = isTerminal

func TestMain(m *testing.M) {
	isTerminal = func(io.Reader) bool { return true }
	os.Exit(m.Run())
}
