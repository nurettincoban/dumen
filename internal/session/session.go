// Package session keeps the small per-session state hooks need between invocations:
// which directives were delivered, which paths were already annotated, and so on.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// State is stored at <sessions dir>/<session id>.json.
type State struct {
	Session      string   `json:"session"`
	Started      string   `json:"started,omitempty"`
	Delivered    []string `json:"delivered,omitempty"`     // directive ids shown to the agent
	Acked        []string `json:"acked,omitempty"`         // directive ids the agent acknowledged
	Nudged       []string `json:"nudged,omitempty"`        // directive ids the stop hook already asked about
	Reminded     []string `json:"reminded,omitempty"`      // paths already annotated with linked records
	Applied      []string `json:"applied,omitempty"`       // decision ids surfaced for edited paths
	LastProgress string   `json:"last_progress,omitempty"` // time of the last progress event
	LastSummary  string   `json:"last_summary,omitempty"`  // sanitized tail of the last assistant message
	Private      bool     `json:"private,omitempty"`       // session is not monitored
	Finished     bool     `json:"finished,omitempty"`

	path string
}

func safe(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		s = "unknown"
	}
	return s
}

// ID returns the file-safe form of a Claude Code session id, as used in activity file names.
func ID(raw string) string { return safe(raw) }

// Load reads a session's state; a missing file yields an empty state.
func Load(dir, id string) *State {
	id = safe(id)
	s := &State{Session: id, path: filepath.Join(dir, id+".json")}
	if data, err := os.ReadFile(s.path); err == nil {
		json.Unmarshal(data, s)
		s.Session = id
	}
	return s
}

// Save writes the state atomically.
func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := s.path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Has reports whether list contains v.
func Has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Add appends v to list if it is missing.
func Add(list []string, v string) []string {
	if Has(list, v) {
		return list
	}
	return append(list, v)
}

// BindParent records that the process with the given pid (the Claude Code process) is
// running this session, so the MCP server, a sibling process, can find its session.
func BindParent(dir string, ppid int, id string) error {
	p := filepath.Join(dir, "by-parent")
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p, strconv.Itoa(ppid)), []byte(safe(id)), 0o644)
}

// ForParent returns the session bound to the given parent pid ("" when unknown).
func ForParent(dir string, ppid int) string {
	data, err := os.ReadFile(filepath.Join(dir, "by-parent", strconv.Itoa(ppid)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
