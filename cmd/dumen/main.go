// Command dumen steers and monitors a team's Claude Code agents through a branch in
// the git remote the team already uses.
package main

import (
	"os"

	"github.com/nurettincoban/comm-pro/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
