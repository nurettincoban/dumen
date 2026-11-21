package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/nurettincoban/dumen/internal/proc"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A ticket is how the PreToolUse hook tells the MCP server that a human-only tool call
// went through the hook and was sent to the permission prompt. The hook writes the ticket
// when it answers "ask", which is before the person decides, so a ticket alone proves
// nothing: a declined call leaves one behind. What makes it safe is who may spend it.
// Only the MCP server that Claude Code itself started for this session can, because
// Claude Code calls that server only after the person said yes:
//
//   - the ticket names the Claude Code process (the hook's parent) and the session;
//   - a server spends it only if it has the same parent, and
//   - only if it is the first live dumen server registered for that parent, which is the
//     one Claude Code started at session start, and
//   - only if it is the very server that was registered when the hook asked. A second
//     `dumen mcp` launched later from a shell, even with the same parent, and even after
//     killing the real one to take over its registration, cannot spend that ticket.
//
// A setup where the dumen hooks are missing cannot approve or publish at all.

// TicketTTL is how long a ticket stays valid: long enough for a person to read the prompt.
const TicketTTL = 10 * time.Minute

type ticket struct {
	Parent  int    `json:"parent"`
	Session string `json:"session"`
	Server  int    `json:"server"` // the dumen MCP server registered when the hook asked
}

func ticketPath(dir, tool string, rawArgs []byte) string {
	if i := strings.LastIndex(tool, "__"); i >= 0 {
		tool = tool[i+2:]
	}
	var v any
	canon := rawArgs
	if json.Unmarshal(rawArgs, &v) == nil {
		if c, err := json.Marshal(v); err == nil {
			canon = c // map keys are sorted, whitespace is gone
		}
	}
	sum := sha256.Sum256(append([]byte(tool+"\n"), canon...))
	return filepath.Join(dir, "tickets", hex.EncodeToString(sum[:16]))
}

// GrantTicket records that the hook running under the Claude Code process parent, in the
// given session, asked the human about this exact tool call.
func GrantTicket(dir, tool string, rawArgs []byte, parent int, sessionID string) error {
	if parent <= 0 {
		return fmt.Errorf("no parent process to bind the ticket to")
	}
	p := ticketPath(dir, tool, rawArgs)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Bind the ticket to the server that is registered right now. If that server dies and
	// another one takes over the registration afterwards, this ticket is not for it.
	server := registeredServer(dir, parent)
	if server <= 0 {
		return fmt.Errorf("no dumen MCP server is registered for this Claude Code process")
	}
	data, _ := json.Marshal(ticket{Parent: parent, Session: sessionID, Server: server})
	return os.WriteFile(p, data, 0o600)
}

// RevokeTicket removes any ticket for this exact tool call.
func RevokeTicket(dir, tool string, rawArgs []byte) {
	os.Remove(ticketPath(dir, tool, rawArgs))
}

// ConsumeTicket reports whether a fresh ticket exists for this exact tool call that the
// server with process id pid, started by parent, may spend. The ticket is removed either way.
func ConsumeTicket(dir, tool string, rawArgs []byte, now time.Time, parent, pid int) bool {
	p := ticketPath(dir, tool, rawArgs)
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	data, _ := os.ReadFile(p)
	os.Remove(p)
	var t ticket
	if json.Unmarshal(data, &t) != nil || t.Parent <= 0 || t.Parent != parent {
		return false
	}
	if now.Sub(st.ModTime()) > TicketTTL {
		return false
	}
	if bound := ForParent(dir, parent); bound != "" && t.Session != "" && bound != safe(t.Session) {
		return false // the parent pid now belongs to a different session
	}
	return t.Server == pid && registeredServer(dir, parent) == pid
}

func serverPath(dir string, parent int) string {
	return filepath.Join(dir, "by-parent", strconv.Itoa(parent)+".mcp")
}

func registeredServer(dir string, parent int) int {
	data, err := os.ReadFile(serverPath(dir, parent))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

// RegisterServer records pid as the dumen MCP server of the Claude Code process parent.
// The first live server wins: it reports false when another live process already holds
// the registration, and that later server can never spend tickets.
func RegisterServer(dir string, parent, pid int) bool {
	if parent <= 0 || pid <= 0 {
		return false
	}
	if err := os.MkdirAll(filepath.Join(dir, "by-parent"), 0o755); err != nil {
		return false
	}
	path := serverPath(dir, parent)
	for i := 0; i < 3; i++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.WriteString(strconv.Itoa(pid))
			f.Close()
			return true
		}
		held := registeredServer(dir, parent)
		if held == pid {
			return true
		}
		if held > 0 && proc.Alive(held) {
			return false
		}
		os.Remove(path) // the earlier server is gone (the parent pid was reused)
	}
	return false
}
