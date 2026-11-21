package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The plugin directory ships the same skill and hooks that `dumen install` writes. Run
// `DUMEN_UPDATE_PLUGIN=1 go test ./internal/install` after changing either.
func TestPluginFilesMatchInstall(t *testing.T) {
	root := filepath.Join("..", "..", "plugin")
	hooks, err := json.MarshalIndent(map[string]any{"hooks": Hooks(PluginCommand)}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	hooks = append(hooks, '\n')
	mcp, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{
		"dumen": map[string]any{"command": PluginCommand, "args": []string{"mcp"}},
	}}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{
		filepath.Join(root, "skills", "dumen", "SKILL.md"): Skill,
		filepath.Join(root, "hooks", "hooks.json"):         hooks,
		filepath.Join(root, ".mcp.json"):                   append(mcp, '\n'),
	}
	for path, content := range want {
		if os.Getenv("DUMEN_UPDATE_PLUGIN") != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), content) {
			t.Errorf("%s is out of date; run DUMEN_UPDATE_PLUGIN=1 go test ./internal/install", path)
		}
	}
}

func TestWriteSettingsKeepsOtherHooks(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
	existing := `{"permissions":{"allow":["Bash(go test *)"]},"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"./lint.sh"}]}]}}`
	os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(existing), 0o644)
	for i := 0; i < 2; i++ { // running install twice must not duplicate dumen's entries
		if _, err := All(dir, "dumen"); err != nil {
			t.Fatal(err)
		}
	}
	var got struct {
		Permissions map[string]any
		Hooks       map[string][]struct {
			Matcher string
			Hooks   []struct {
				Command string
				Args    []string
			}
		}
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Permissions == nil {
		t.Error("existing settings were dropped")
	}
	pre := got.Hooks["PreToolUse"]
	if len(pre) != 2 || pre[0].Hooks[0].Command != "./lint.sh" || pre[1].Matcher != "*" || pre[1].Hooks[0].Args[1] != "pre-tool-use" {
		t.Errorf("PreToolUse after two installs: %+v", pre)
	}
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		if len(got.Hooks[ev]) != 1 {
			t.Errorf("%s: want exactly one dumen entry, got %d", ev, len(got.Hooks[ev]))
		}
	}
	var mcp struct {
		McpServers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	data, _ = os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if json.Unmarshal(data, &mcp) != nil || mcp.McpServers["dumen"].Command != "dumen" {
		t.Errorf(".mcp.json: %s", data)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "dumen", "SKILL.md")); err != nil {
		t.Error("skill not written")
	}
}

func TestWriteSettingsPreApprovesAgentToolsOnly(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 2; i++ { // twice: it must be idempotent
		if _, err := WriteSettings(dir, "dumen"); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Count(text, `"mcp__dumen__ack_directive"`) != 1 {
		t.Fatalf("ack_directive must be pre-approved exactly once:\n%s", text)
	}
	for _, human := range []string{"approve", "supersede", "publish_directive"} {
		if strings.Contains(text, `"mcp__dumen__`+human+`"`) {
			t.Fatalf("%s must never be pre-approved", human)
		}
	}
}
