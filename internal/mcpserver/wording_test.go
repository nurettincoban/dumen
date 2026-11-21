package mcpserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// Gauntlet D2: what agents read must not send them, or their human, to a terminal. The
// commands are the agent's; human actions go through the tools and the permission prompt.
func TestNoTerminalCommandsInAgentText(t *testing.T) {
	forbidden := []string{"dumen approve", "dumen reject", "dumen role set", "dumen direct", "dumen supersede", "dumen ref check", "dumen withdraw", "dumen private", "dumen product", "own terminal"}
	for _, dir := range []string{".", "../hook"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				for _, w := range forbidden {
					if strings.Contains(strings.ToLower(lit.Value), w) {
						t.Errorf("%s: %q points to a terminal command", fset.Position(lit.Pos()), w)
					}
				}
				return true
			})
		}
	}
}
