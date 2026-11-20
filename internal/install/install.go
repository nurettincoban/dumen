// Package install writes the Claude Code configuration that connects a repository to dumen.
package install

import (
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
		"Stop":       {{Hooks: h("stop", 5)}},
		"SessionEnd": {{Hooks: h("session-end", 3)}},
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
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, append(data, '\n'), 0o644)
}
