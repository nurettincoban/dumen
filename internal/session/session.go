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
	Session   string   `json:"session"`
	Started   string   `json:"started,omitempty"`
	Delivered []string `json:"delivered,omitempty"` // directive ids shown to the main agent
	// SubDelivered holds the same per subagent (by agent_id): a subagent has its own context.
	SubDelivered map[string][]string `json:"sub_delivered,omitempty"`
	Acked        []string            `json:"acked,omitempty"`         // directive ids the agent acknowledged
	Nudged       []string            `json:"nudged,omitempty"`        // directive ids the stop hook already asked about
	Reminded     []string            `json:"reminded,omitempty"`      // paths already annotated with linked records
	Applied      []string            `json:"applied,omitempty"`       // decision ids surfaced for edited paths
	LastProgress string              `json:"last_progress,omitempty"` // time of the last progress event
	LastSummary  string              `json:"last_summary,omitempty"`  // sanitized tail of the last assistant message
	Private      bool                `json:"private,omitempty"`       // session is not monitored
	Finished     bool                `json:"finished,omitempty"`
	Asking       bool                `json:"asking,omitempty"`    // the agent said it is waiting for its human
	Violated     []string            `json:"violated,omitempty"`  // "<directive id> <file>" already reported as changed under a deny directive
	Held         []string            `json:"held,omitempty"`      // keys of questions held back once because a decision may answer them
	Answers      []Answer            `json:"answers,omitempty"`   // the last questions the agent asked its human, with the raw answer
	Noted        []string            `json:"noted,omitempty"`     // decision ids already in the agent's context (session start, topic notes)
	NewsBase     bool                `json:"news_base,omitempty"` // Known holds the baseline, so later decisions are news
	SubKnown     map[string][]string `json:"sub_known,omitempty"` // decision news given to each subagent (by agent_id)
	Known        []string            `json:"known,omitempty"`     // decision ids in force when the session started, or delivered as news since

	path string
}

// Answer is one call of Claude Code's question tool as the PostToolUse hook saw it.
// Raw keeps what Claude Code returned for the call as it was sent, because the shape of
// the answer is not documented: dumen only searches it for the picked label.
type Answer struct {
	At        string     `json:"at"`
	ToolUseID string     `json:"tool_use_id,omitempty"`
	Questions []Question `json:"questions"`
	// Picked holds the labels Claude Code reported per question, when it reported them
	// in a structured "answers" field.
	Picked map[string][]string `json:"picked,omitempty"`
	Raw    string              `json:"raw"`
}

// Question is one question of a question-tool call.
type Question struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// MaxAnswers is how many question-tool calls a session keeps.
const MaxAnswers = 10

// AddAnswer keeps the newest MaxAnswers calls.
func (s *State) AddAnswer(a Answer) {
	s.Answers = append(s.Answers, a)
	if n := len(s.Answers); n > MaxAnswers {
		s.Answers = s.Answers[n-MaxAnswers:]
	}
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

// MarkApproved records that the dumen server of session id put decision did in force,
// in a file of its own so that it never races the hooks' writes to the session state.
func MarkApproved(dir, id, did string) error {
	if id == "" || did == "" {
		return nil
	}
	p := filepath.Join(dir, safe(id)+".approved")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(did + "\n")
	return err
}

// Approved lists the decisions the dumen server of session id put in force.
func Approved(dir, id string) []string {
	if id == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, safe(id)+".approved"))
	if err != nil {
		return nil
	}
	return strings.Fields(string(data))
}
