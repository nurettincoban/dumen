// Package install writes the Claude Code configuration that connects a repository to dumen.
package install

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type handler struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Timeout int      `json:"timeout"`
}

type group struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []handler `json:"hooks"`
}

// Hooks returns the hooks section dumen needs. Handlers use exec form (command plus
// args), so no shell is started and the same configuration works on Windows.
func Hooks(binary string) map[string][]group {
	h := func(event string, timeout int) []handler {
		return []handler{{Type: "command", Command: binary, Args: []string{"hook", event}, Timeout: timeout}}
	}
	return map[string][]group{
		"SessionStart":     {{Hooks: h("session-start", 10)}},
		"UserPromptSubmit": {{Hooks: h("user-prompt-submit", 5)}},
		// One handler for every tool: it enforces deny directives on edits, guards the
		// human-only dumen tools and shell commands, and delivers new directives.
		"PreToolUse": {{Matcher: "*", Hooks: h("pre-tool-use", 5)}},
		// Keeps the questions the agent asked its human and the answers, so that an
		// answer can be saved as a team decision.
		"PostToolUse": {{Matcher: "AskUserQuestion", Hooks: h("post-tool-use", 5)}},
		"Stop":        {{Hooks: h("stop", 5)}},
		"SessionEnd":  {{Hooks: h("session-end", 3)}},
	}
}

// isDumen reports whether a hook group in an existing settings file was written by dumen.
func isDumen(g any) bool {
	m, ok := g.(map[string]any)
	if !ok {
		return false
	}
	hooks, _ := m["hooks"].([]any)
	for _, h := range hooks {
		hm, _ := h.(map[string]any)
		args, _ := hm["args"].([]any)
		if len(args) > 0 && args[0] == "hook" {
			if cmd, _ := hm["command"].(string); filepath.Base(cmd) == "dumen" || filepath.Base(cmd) == "dumen.exe" {
				return true
			}
		}
	}
	return false
}

// AgentTools are the MCP tools an agent may call without asking its human.
var AgentTools = []string{
	"directives", "ack_directive", "propose_directive",
	"search_decisions", "get_decision", "decisions_for_path", "decisions_for_topic", "draft_decision", "record_answer",
	"add_ref", "log_event", "activity",
}

// WriteSettings adds dumen's hooks to <dir>/.claude/settings.json, keeping everything
// else in the file. Running it again replaces dumen's own entries and nothing else.
func WriteSettings(dir, binary string) (string, error) {
	path := filepath.Join(dir, ".claude", "settings.json")
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return path, fmt.Errorf("%s is not valid JSON: %v", path, err)
		}
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for event, groups := range Hooks(binary) {
		var kept []any
		if existing, ok := hooks[event].([]any); ok {
			for _, g := range existing {
				if !isDumen(g) {
					kept = append(kept, g)
				}
			}
		}
		for _, g := range groups {
			kept = append(kept, g)
		}
		hooks[event] = kept
	}
	settings["hooks"] = hooks

	// Pre-approve the tools an agent may always use, so that acknowledging a directive
	// never stops a session on a permission prompt. approve, supersede and
	// publish_directive are deliberately absent: those always ask the human.
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	have := map[string]bool{}
	for _, a := range allow {
		if str, ok := a.(string); ok {
			have[str] = true
		}
	}
	for _, tool := range AgentTools {
		if name := "mcp__dumen__" + tool; !have[name] {
			allow = append(allow, name)
		}
	}
	perms["allow"] = allow
	settings["permissions"] = perms

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

// Skill is the role-aware dumen skill for Claude Code.
//
//go:embed assets/SKILL.md
var Skill []byte

// WriteSkill writes <dir>/.claude/skills/dumen/SKILL.md.
func WriteSkill(dir string) (string, error) {
	path := filepath.Join(dir, ".claude", "skills", "dumen", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, Skill, 0o644)
}

// WriteMCP registers the dumen MCP server in <dir>/.mcp.json, keeping other servers.
func WriteMCP(dir, binary string) (string, error) {
	path := filepath.Join(dir, ".mcp.json")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return path, fmt.Errorf("%s is not valid JSON: %v", path, err)
		}
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	// The key must be "dumen": hooks match the tool names mcp__dumen__<tool>.
	servers["dumen"] = map[string]any{"command": binary, "args": []string{"mcp"}}
	cfg["mcpServers"] = servers
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return path, err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}

// All writes hooks, the MCP registration and the skill into dir.
func All(dir, binary string) ([]string, error) {
	var written []string
	for _, f := range []func() (string, error){
		func() (string, error) { return WriteSettings(dir, binary) },
		func() (string, error) { return WriteMCP(dir, binary) },
		func() (string, error) { return WriteSkill(dir) },
	} {
		p, err := f()
		if err != nil {
			return written, err
		}
		written = append(written, p)
	}
	return written, nil
}

// PluginCommand is how the Claude Code plugin starts dumen: the native binary that
// bin/dumen-install.cjs put in the plugin's data directory. Hooks and the MCP server run it
// directly, never through a wrapper, so dumen's parent process is Claude Code itself: dumen
// binds sessions and approval tickets to that parent. On Windows the file is dumen.exe,
// which an extensionless absolute path resolves to.
const PluginCommand = "${CLAUDE_PLUGIN_DATA}/dumen"
