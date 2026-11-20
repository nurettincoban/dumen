package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A ticket is how the PreToolUse hook tells the MCP server that a human-only tool call
// went through the hook and was sent to the permission prompt. The hook writes the ticket
// when it answers "ask"; the tool only runs if Claude Code then lets the call through,
// and the MCP server refuses to act without a fresh ticket for exactly that call. So a
// setup where the dumen hooks are missing cannot approve or publish at all.

// TicketTTL is how long a ticket stays valid: long enough for a person to read the prompt.
const TicketTTL = 10 * time.Minute

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

// GrantTicket records that the hook asked the human about this exact tool call.
func GrantTicket(dir, tool string, rawArgs []byte) error {
	p := ticketPath(dir, tool, rawArgs)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o644)
}

// ConsumeTicket reports whether a fresh ticket exists for this exact tool call, and removes it.
func ConsumeTicket(dir, tool string, rawArgs []byte, now time.Time) bool {
	p := ticketPath(dir, tool, rawArgs)
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	os.Remove(p)
	return now.Sub(st.ModTime()) <= TicketTTL
}
