//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// service is one repository of the product, on its own developer's machine, sharing the
// team's dumen store with the others.
func service(t *testing.T, w *world, store, name, email string) *world {
	t.Helper()
	tmp := t.TempDir()
	s := &world{t: t, bin: w.bin, repo: filepath.Join(tmp, name)}
	for _, kv := range w.env {
		if !strings.HasPrefix(kv, "DUMEN_HOME=") {
			s.env = append(s.env, kv)
		}
	}
	s.env = append(s.env, "DUMEN_HOME="+filepath.Join(tmp, "dumen-home"), "DUMEN_POLL_INTERVAL=3s")
	origin := filepath.Join(tmp, name+".git")
	s.sh(tmp, "git", "init", "-q", "--bare", origin)
	s.sh(tmp, "git", "init", "-q", s.repo)
	s.sh(s.repo, "git", "config", "user.email", email)
	s.sh(s.repo, "git", "config", "user.name", strings.Split(email, "@")[0])
	s.sh(s.repo, "git", "remote", "add", "origin", origin)
	return s
}

// claudeRecorded runs headless Claude Code and returns its final answer and a transcript
// of its tool calls, in the shape Claude Code shows them.
func (w *world) claudeRecorded(prompt string, extra ...string) (string, string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args := []string{"-p", prompt, "--model", model(), "--permission-mode", "acceptEdits", "--output-format", "stream-json", "--verbose"}
	if f := filepath.Join(w.repo, ".mcp.json"); fileExists(f) {
		args = append(args, "--mcp-config", f)
	}
	if f := w.mergedSettings(); f != "" {
		args = append(args, "--settings", f)
	}
	args = append(args, extra...)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir, cmd.Env = w.repo, w.env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		w.t.Fatalf("claude -p: %v\nstderr: %s", err, errb.String())
	}
	var final string
	var tr strings.Builder
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Result  string `json:"result"`
			Message struct {
				Content []struct {
					Type    string          `json:"type"`
					Text    string          `json:"text"`
					Name    string          `json:"name"`
					Input   json.RawMessage `json:"input"`
					Content json.RawMessage `json:"content"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Type == "result" {
			final = ev.Result
		}
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "tool_use":
				fmt.Fprintf(&tr, "⏺ %s(%s)\n", shortTool(c.Name), compact(c.Input))
			case "tool_result":
				fmt.Fprintf(&tr, "  ⎿ %s\n", indent(resultText(c.Content)))
			case "text":
				if s := strings.TrimSpace(c.Text); s != "" {
					fmt.Fprintf(&tr, "⏺ %s\n", indent(s))
				}
			}
		}
	}
	return final, tr.String()
}

func shortTool(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 && strings.HasPrefix(name, "mcp__") {
		return "dumen · " + name[i+2:]
	}
	return name
}

func compact(raw json.RawMessage) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var out []string
	for _, p := range parts {
		out = append(out, p.Text)
	}
	return strings.Join(out, "\n")
}

func indent(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n    ") }

// record keeps a transcript for the docs when DUMEN_E2E_RECORD names a directory.
func record(t *testing.T, name, text string) {
	t.Logf("%s:\n%s", name, text)
	if dir := os.Getenv("DUMEN_E2E_RECORD"); dir != "" {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0o644)
	}
}

// The frontend makes a UI decision that needs work from the backend. The backend agent,
// already in the middle of its own work, hears it in the same session, with the ask.
func TestFrontendDecisionReachesRunningBackendSession(t *testing.T) {
	base := setupWith(t, false) // only for the binary and the person's environment
	store := filepath.Join(t.TempDir(), "store.git")
	base.sh(base.repo, "git", "init", "-q", "--bare", store)

	web := service(t, base, store, "web-app", "sofia@example.com")
	web.write("src/TimePicker.tsx", "export const TimePicker = () => null;\n")
	web.sh(web.repo, "git", "add", "-A")
	web.sh(web.repo, "git", "commit", "-q", "-m", "init")
	web.dumen("init", "--store", store+"#dumen", "--project", "shop")
	web.dumen("install")
	web.person("role", "set", "ayse@example.com", "dev")

	booking := service(t, base, store, "booking-svc", "ayse@example.com")
	booking.write("src/slots.go", "package slots\n\n// List returns the open slots of a day.\nfunc List() []string { return nil }\n")
	booking.write("check.sh", "sleep 35\necho 'go vet: ok'\n")
	booking.sh(booking.repo, "git", "add", "-A")
	booking.sh(booking.repo, "git", "commit", "-q", "-m", "init")
	booking.dumen("init", "--store", store+"#dumen", "--project", "shop")
	booking.dumen("install")

	// The person's "Yes" in the permission prompt.
	web.write(".claude/settings.local.json", `{"hooks":{"PermissionRequest":[{"matcher":"mcp__dumen__approve","hooks":[{"type":"command","command":"printf '{\"hookSpecificOutput\":{\"hookEventName\":\"PermissionRequest\",\"decision\":{\"behavior\":\"allow\"}}}'"}]}]}}`)

	type res struct{ final, tr string }
	done := make(chan res, 1)
	go func() {
		time.Sleep(10 * time.Second) // Ayşe's agent is running check.sh by now
		final, tr := web.claudeRecorded("You are working on the time picker in web-app. You asked me: \"How should the time picker show availability?\" "+
			"and I answered: \"Slot buttons show how many tables are left\". The backend does not return that yet. "+
			"Save my answer as a team decision with the dumen record_answer tool (kind ui, services web-app, topics booking-flow, "+
			"needs from booking-svc: \"Return remaining tables per slot in GET /slots\"), then put it in force with the dumen approve tool. "+
			"Report the decision id.",
			"--allowedTools", "mcp__dumen__record_answer", "--permission-prompts", "none")
		done <- res{final, tr}
	}()
	final, tr := booking.claudeRecorded("Run `sh check.sh` once and wait for it (about 35 seconds). Then read src/slots.go. "+
		"Then tell me in a few lines whether anything from the team changed while you worked and what it means for this service.",
		"--allowedTools", "Bash(sh check.sh)", "Read", "mcp__dumen__news", "mcp__dumen__get_decision")
	web1 := <-done
	record(t, "frontend-web-app", web1.tr)
	record(t, "frontend-booking-svc", tr)
	if !strings.Contains(final, "remaining") && !strings.Contains(final, "tables") {
		t.Errorf("the booking-svc session did not hear what web-app needs from it:\n%s", final)
	}
	if !strings.Contains(web1.final, "D-") {
		t.Errorf("the web-app session did not report a decision id:\n%s", web1.final)
	}
}

// An agent about to ask its human a question the team already settled finds the decision
// and follows it.
func TestAgentLooksBeforeAsking(t *testing.T) {
	w := setup(t)
	id := firstID(w.dumen("decide", "--title", "Refunds for cancelled bookings", "--rule", "Cancelled bookings are refunded as store credit",
		"--kind", "product", "--topics", "payments", "-m", "Store credit keeps the money in the product. Rejected: refund to the card."))
	w.person("approve", id)
	final, tr := w.claudeRecorded("I am building the cancel endpoint. Before you ask me whether a cancelled booking is refunded to the card "+
		"or as store credit, check whether the team already decided it, then tell me what you will do and which decision you follow.",
		"--allowedTools", "mcp__dumen__decisions_for_topic", "mcp__dumen__search_decisions", "mcp__dumen__get_decision")
	record(t, "look-before-asking", tr)
	if !strings.Contains(final, id) {
		t.Errorf("the agent did not name %s:\n%s", id, final)
	}
}

// Setup is the agent's: the person asks, the agent runs dumen from its shell, and a team
// change goes through the permission prompt.
func TestAgentSetsUpTheTeam(t *testing.T) {
	base := setupWith(t, false)
	store := filepath.Join(t.TempDir(), "store.git")
	base.sh(base.repo, "git", "init", "-q", "--bare", store)
	w := service(t, base, store, "booking-svc", "maya@example.com")
	w.write("src/slots.go", "package slots\n")
	w.sh(w.repo, "git", "add", "-A")
	w.sh(w.repo, "git", "commit", "-q", "-m", "init")
	plugin, err := filepath.Abs(filepath.Join("..", "..", "plugin"))
	if err != nil {
		t.Fatal(err)
	}
	_, tr := w.claudeRecorded("Set up dumen for our team in this repository. The team store is "+store+"#dumen and the project is called shop. "+
		"Use the dumen CLI from the shell for the setup, then tell me in two lines what happened.",
		"--plugin-dir", plugin, "--allowedTools", "Bash(dumen *)")
	record(t, "setup-init", strings.ReplaceAll(tr, filepath.Dir(store), "/srv/git"))
	if !fileExists(filepath.Join(w.repo, ".dumen")) {
		t.Fatal("the agent did not set up dumen")
	}
	w.write(".claude/settings.local.json", `{"hooks":{"PermissionRequest":[{"matcher":"mcp__plugin_dumen_dumen__set_role","hooks":[{"type":"command","command":"printf '{\"hookSpecificOutput\":{\"hookEventName\":\"PermissionRequest\",\"decision\":{\"behavior\":\"allow\"}}}'"}]}]}}`)
	_, tr = w.claudeRecorded("Add ayse@example.com to the dumen team as a dev.", "--plugin-dir", plugin, "--permission-prompts", "none")
	record(t, "setup-role", tr)
	if out := w.dumen("role", "list"); !strings.Contains(out, "ayse@example.com") {
		t.Errorf("ayse was not added:\n%s", out)
	}
}
