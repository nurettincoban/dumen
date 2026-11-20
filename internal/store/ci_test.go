package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The store branch must be refused on any host, not only GitLab.
func TestCIConfigIsHostAgnostic(t *testing.T) {
	if got := ciConfig(t.TempDir()); got != "" {
		t.Fatalf("empty tree: %q", got)
	}
	for _, f := range []string{".gitlab-ci.yml", ".github/workflows/ci.yml", "bitbucket-pipelines.yml", ".gitea/workflows/x.yml", "Jenkinsfile"} {
		root := t.TempDir()
		path := filepath.Join(root, filepath.FromSlash(f))
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ciConfig(root) == "" {
			t.Errorf("%s was not recognised as a pipeline definition", f)
		}
	}
}
