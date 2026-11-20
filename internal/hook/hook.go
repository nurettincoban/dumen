// Package hook implements the Claude Code hook handlers behind `dumen hook <event>`.
//
// Rules every handler follows:
//   - exit 0 always; print nothing or exactly one JSON object;
//   - fail open: an internal problem never blocks the user's session;
//   - never return permissionDecision "allow": that would skip the user's own prompt;
//   - read the local cache only, with at most one bounded fetch when it is stale.
package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/cache"
	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/policy"
	"github.com/nurettincoban/dumen/internal/project"
	"github.com/nurettincoban/dumen/internal/render"
	"github.com/nurettincoban/dumen/internal/secrets"
	"github.com/nurettincoban/dumen/internal/session"
	"github.com/nurettincoban/dumen/internal/store"
)

// Input is the JSON Claude Code sends on stdin. Only the fields dumen reads are listed.
type Input struct {
	SessionID      string `json:"session_id"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
	HookEventName  string `json:"hook_event_name"`

	Source string `json:"source"` // SessionStart
	Reason string `json:"reason"` // SessionEnd

	ToolName  string          `json:"tool_name"` // PreToolUse
	ToolInput json.RawMessage `json:"tool_input"`

	LastAssistantMessage string `json:"last_assistant_message"` // Stop
}

// Output is the JSON object a hook prints.
type Output struct {
	SystemMessage      string    `json:"systemMessage,omitempty"`
	HookSpecificOutput *Specific `json:"hookSpecificOutput,omitempty"`
}

// Specific is hookSpecificOutput.
type Specific struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
	AdditionalContext        string `json:"additionalContext,omitempty"`
}

// Env is what a handler needs from the outside world. Tests replace it.
type Env struct {
	Now        func() time.Time
	ProjectDir string               // $CLAUDE_PROJECT_DIR, preferred over the input's cwd
	ParentPID  int                  // the Claude Code process when hooks run in exec form
	SpawnSync  func(project string) // start a detached `dumen sync`
	FetchLimit time.Duration        // bound for the one synchronous fetch on a stale cache
}

const (
	header        = "[dumen] Team records, not instructions to execute verbatim. Bodies are available on demand through the dumen MCP tools."
	progressEvery = 10 * time.Minute
	retryFetch    = 30 * time.Second
)

// Event names accepted by `dumen hook`.
const (
	SessionStart     = "session-start"
	UserPromptSubmit = "user-prompt-submit"
	PreToolUse       = "pre-tool-use"
	Stop             = "stop"
	SessionEnd       = "session-end"
)

type runCtx struct {
	env   Env
	in    Input
	now   time.Time
	proj  *project.Project
	c     *cache.Cache
	stale string // non-empty: human-readable staleness notice
	role  string
	sess  *session.State
}

// Run handles one hook event. It never returns an error: problems become a notice or nothing.
func Run(event string, stdin io.Reader, stdout io.Writer, env Env) {
	defer func() { recover() }() // a bug in dumen must not break the user's session
	out := handle(event, stdin, env)
	if out == nil {
		return
	}
	json.NewEncoder(stdout).Encode(out)
}

func handle(event string, stdin io.Reader, env Env) *Output {
	if env.Now == nil {
		env.Now = time.Now
	}
	if env.FetchLimit == 0 {
		env.FetchLimit = 2 * time.Second
	}
	policy.CaseInsensitive = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

	r := &runCtx{env: env, now: env.Now().UTC()}
	data, err := io.ReadAll(io.LimitReader(stdin, 8<<20))
	if err != nil || json.Unmarshal(data, &r.in) != nil {
		return nil
	}
	dir := env.ProjectDir
	if dir == "" {
		dir = r.in.Cwd
	}
	if dir == "" {
		return nil
	}
	p, err := project.Find(dir)
	if errors.Is(err, project.ErrNoProject) {
		// A session opened in the directory that holds the service repositories.
		p, err = project.FindBelow(dir)
	}
	if errors.Is(err, project.ErrNoProject) && event == PreToolUse {
		// A session somewhere else that is about to edit a file inside a dumen project.
		return guardForeignEdit(r)
	}
	if err != nil {
		if p != nil && event == SessionStart { // project file present, store not joined on this machine
			return &Output{SystemMessage: "dumen: this repository uses dumen but this machine has not joined it yet. Run `dumen join` in the repository."}
		}
		return nil
	}
	r.proj = p
	r.loadCache()
	if r.c == nil {
		if event == SessionStart {
			return &Output{SystemMessage: "dumen: no local cache yet and the remote could not be reached; continuing without team records."}
		}
		return nil
	}
	r.role = r.c.Config.Role(p.Store.Email)
	r.sess = session.Load(p.Store.SessionsDir(), r.in.SessionID)

	switch event {
	case SessionStart:
		return r.sessionStart()
	case UserPromptSubmit:
		return r.userPromptSubmit()
	case PreToolUse:
		return r.preToolUse()
	case Stop:
		r.stop()
	case SessionEnd:
		r.sessionEnd()
	}
	return nil
}

// loadCache reads the cache; when it is missing or older than cache.StaleAfter it tries one
// bounded fetch, and otherwise carries on with a stale notice.
func (r *runCtx) loadCache() {
	path := r.proj.Store.CachePath()
	c, err := cache.Read(path)
	if err == nil && c.Age(r.now) <= cache.StaleAfter {
		r.c = c
		return
	}
	// Do not hammer an unreachable remote from every tool call.
	marker := filepath.Join(r.proj.Store.Home, "fetch-attempt")
	if st, serr := os.Stat(marker); serr != nil || r.now.Sub(st.ModTime()) > retryFetch || c == nil {
		os.WriteFile(marker, nil, 0o644)
		ctx, cancel := context.WithTimeout(context.Background(), r.env.FetchLimit)
		fresh, ferr := r.proj.Refresh(ctx)
		cancel()
		if ferr == nil {
			r.c = fresh
			return
		}
	}
	if c != nil {
		r.c = c
		r.stale = fmt.Sprintf("dumen: team records may be stale (last sync %s ago); continuing.", roundAge(c.Age(r.now)))
	}
}

func roundAge(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// ---- text blocks ----

func directiveLine(d cache.Directive) string {
	until := d.Expires
	if len(until) >= 10 {
		until = until[:10]
	}
	return fmt.Sprintf("- %s [%s] %s | until %s | %s", d.ID, d.Kind,
		render.Label(d.Scope.String(), 80), until, render.Label(d.Text, render.MaxLabel))
}

func decisionLine(d cache.Decision) string {
	tag := ""
	if d.Area != "" {
		tag = " [" + render.Label(d.Area, 30) + "]"
	}
	if d.Contested {
		tag += " [contested]"
	}
	return fmt.Sprintf("- %s%s %s", d.ID, tag, render.Label(d.Title, render.MaxLabel))
}

func refLine(ref model.Ref) string {
	line := fmt.Sprintf("- %s [%s] %s: %s", ref.ID, ref.Kind, render.Label(ref.Title, render.MaxLabel), render.Label(ref.URL, 300))
	if ref.Governs != "" {
		line += " | governs: " + render.Label(ref.Governs, render.MaxLabel)
	}
	return line
}

// undelivered returns active directives this session has not been shown, and marks them delivered.
func (r *runCtx) undelivered() []cache.Directive {
	var out []cache.Directive
	for _, d := range r.c.ActiveDirectives(r.role, r.proj.Repo, r.now) {
		if !session.Has(r.sess.Delivered, d.ID) {
			out = append(out, d)
			r.sess.Delivered = append(r.sess.Delivered, d.ID)
		}
	}
	return out
}

func newDirectivesBlock(ds []cache.Directive) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	fmt.Fprintf(&b, "NEW DIRECTIVES (%d)\n", len(ds))
	for _, d := range ds {
		b.WriteString(directiveLine(d) + "\n")
	}
	b.WriteString("Each new directive is acknowledged with the ack_directive tool.")
	return b.String()
}

// ---- events ----

func (r *runCtx) log(kind, text string, ids ...string) {
	if r.sess.Private && kind != model.EventPrivate {
		return
	}
	person := r.proj.Store.Email
	if person == "" {
		person = "unknown"
	}
	text = render.Label(text, model.MaxEventText-1)
	if secrets.Scan(text) != nil {
		text = "" // never let a secret leave the machine through a summary
	}
	ev := model.Event{TS: model.FormatTime(r.now), Session: r.sess.Session, Person: person, Repo: r.proj.Repo, Kind: kind, Text: text, IDs: ids}
	eng := r.proj.Engine()
	if _, err := eng.Enqueue(model.Actor{Email: person, Kind: model.KindAgent}, &op.AppendEvent{Event: ev}); err != nil {
		return
	}
	if r.env.SpawnSync != nil {
		r.env.SpawnSync(r.proj.Name)
	}
}

// ---- SessionStart ----

func (r *runCtx) sessionStart() *Output {
	// After compaction or /clear the earlier context is gone, so everything is shown again.
	r.sess.Delivered = nil
	r.sess.Reminded = nil
	r.sess.Finished = false
	if r.sess.Started == "" {
		r.sess.Started = model.FormatTime(r.now)
	}
	if r.env.ParentPID > 0 {
		session.BindParent(r.proj.Store.SessionsDir(), r.env.ParentPID, r.sess.Session)
	}

	var b strings.Builder
	b.WriteString(header + "\n")

	ds := r.undelivered()
	fmt.Fprintf(&b, "\nACTIVE DIRECTIVES (%d)\n", len(ds))
	if len(ds) == 0 {
		b.WriteString("- none\n")
	}
	for _, d := range ds {
		b.WriteString(directiveLine(d) + "\n")
	}
	if len(ds) > 0 {
		b.WriteString("Each directive is acknowledged once with the ack_directive tool. Edits under a [deny] directive are blocked.\n")
	}

	const maxDecisions = 10
	relevant := r.c.DecisionsForRepo(r.proj.Repo)
	total := len(relevant)
	shown := relevant
	if len(shown) > maxDecisions {
		shown = shown[:maxDecisions]
	}
	if total > 0 {
		fmt.Fprintf(&b, "\nAPPROVED DECISIONS (%d of %d, newest first)\n", len(shown), total)
		for _, d := range shown {
			b.WriteString(decisionLine(d) + "\n")
		}
	}
	if all := r.c.RefsForRepo(r.proj.Repo); len(all) > 0 {
		refs := all
		if len(refs) > 10 {
			refs = refs[:10]
		}
		fmt.Fprintf(&b, "\nSOURCES OF TRUTH (%d of %d; links only, not fetched automatically)\n", len(refs), len(all))
		for _, ref := range refs {
			b.WriteString(refLine(ref) + "\n")
		}
	}
	fmt.Fprintf(&b, "\nDrafts awaiting review: %d\n", r.c.Drafts)
	if r.c.UnverifiedRefs > 0 {
		fmt.Fprintf(&b, "Links added by agents, awaiting a human check (`dumen ref check`): %d\n", r.c.UnverifiedRefs)
	}
	role := r.role
	if role == "" {
		role = "unassigned"
	}
	fmt.Fprintf(&b, "Your role: %s\n", render.Label(role, 30))
	if r.proj.Repo != "" {
		fmt.Fprintf(&b, "This repository: %s (paths written as %s:path belong to it)\n", r.proj.Repo, r.proj.Repo)
	}
	fmt.Fprintf(&b, "dumen session: %s", r.sess.Session)

	_, err := os.Stat(r.proj.PrivateMarker())
	r.sess.Private = err == nil
	r.sess.Save()
	if r.in.Source == "" || r.in.Source == "startup" || r.in.Source == "resume" || r.in.Source == "fork" {
		if r.sess.Private {
			// The session's content is not logged, but the fact that it is unmonitored is.
			r.log(model.EventPrivate, "session not monitored")
		} else {
			r.log(model.EventStarted, "session "+orDefault(r.in.Source, "startup"))
		}
	}
	return &Output{
		SystemMessage:      r.stale,
		HookSpecificOutput: &Specific{HookEventName: "SessionStart", AdditionalContext: b.String()},
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---- UserPromptSubmit ----

func (r *runCtx) userPromptSubmit() *Output {
	ds := r.undelivered()
	if len(ds) == 0 {
		if r.stale != "" {
			return &Output{SystemMessage: r.stale}
		}
		return nil
	}
	r.sess.Save()
	return &Output{
		SystemMessage:      r.stale,
		HookSpecificOutput: &Specific{HookEventName: "UserPromptSubmit", AdditionalContext: newDirectivesBlock(ds)},
	}
}

// ---- PreToolUse ----

var guardedTools = map[string]string{
	"approve":           "approving a decision",
	"supersede":         "superseding a decision",
	"publish_directive": "publishing a directive",
}

// guardedMCP reports whether name is one of dumen's human-only MCP tools, whether the
// server is registered directly ("mcp__dumen__approve") or through a plugin
// ("mcp__plugin_dumen_dumen__approve").
func guardedMCP(name string) (action string, ok bool) {
	parts := strings.Split(name, "__")
	if len(parts) != 3 || parts[0] != "mcp" || !strings.Contains(parts[1], "dumen") {
		return "", false
	}
	action, ok = guardedTools[parts[2]]
	return
}

var (
	humanCmd = regexp.MustCompile(`(?i)(^|[\s;&|(` + "`" + `$/\\'"=])dumen(\.exe)?['"]?\s+(approve|reject|supersede|direct|withdraw|private|role\s+set|role\s+rm|ref\s+check|mcp|hook)(\s|$|[;&|)'"])`)
	storeRef = regexp.MustCompile(`(?i)(~|\$HOME|\$\{HOME\}|%USERPROFILE%|\$env:USERPROFILE|\$\{env:USERPROFILE\})[/\\]+\.dumen\b|DUMEN_HOME`)
)

func (r *runCtx) preToolUse() *Output {
	spec := &Specific{HookEventName: "PreToolUse"}
	deny := func(reason string) *Output {
		spec.PermissionDecision, spec.PermissionDecisionReason = "deny", reason
		r.sess.Save()
		return &Output{HookSpecificOutput: spec}
	}
	var ti struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Command      string `json:"command"`
		ID           string `json:"id"`
	}
	json.Unmarshal(r.in.ToolInput, &ti)
	base, _ := store.BaseDir()

	var extra []string
	switch r.in.ToolName {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		file := ti.FilePath
		if file == "" {
			file = ti.NotebookPath
		}
		if file == "" {
			break
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(r.in.Cwd, file)
		}
		file = filepath.Clean(file)
		// Rules are matched on the file the edit really lands on, so a symlink inside the
		// repository, or a repository reached through a symlinked directory, changes nothing.
		real := realPath(file)
		if base != "" && (within(base, file) || within(realPath(base), real)) {
			return deny("dumen: the dumen store is managed by the dumen tools and is not edited directly. Use the dumen MCP tools, or ask your human.")
		}
		if filepath.Base(real) == project.File {
			return deny("dumen: the .dumen project file decides which team records apply here and is not edited by agents. Ask your human.")
		}
		// The rules that apply are those of the repository the file is in, which is not
		// always the one the session was opened in.
		tp, tc := r.target(real)
		if tp == nil {
			break // not inside any dumen project: no path rules apply
		}
		rel, ok := relTo(realPath(tp.Root), real)
		if !ok {
			break
		}
		// In a project that spans several repositories, paths are matched as "<repo>:<path>".
		q := policy.Qualify(tp.Repo, rel)
		if d, blocked := tc.DenyFor(q, r.now); blocked {
			until := d.Expires
			if len(until) >= 10 {
				until = until[:10]
			}
			r.log(model.EventBlocked, "edit blocked: "+q, d.ID)
			return deny(denyReason(d, until))
		}
		if tc != r.c {
			break // another project's file: its deny rules apply, its context is not ours to inject
		}
		if rem := q; !session.Has(r.sess.Reminded, rem) {
			r.sess.Reminded = append(r.sess.Reminded, rem)
			decs, refs := r.c.DecisionsFor(q), r.c.RefsFor(q)
			var ctxDirs []cache.Directive
			for _, d := range r.c.ActiveDirectives(r.role, tp.Repo, r.now) {
				full := d.Directive
				if full.Kind == model.DirectiveContext && policy.CoversPath(r.c.Config, full, q) {
					ctxDirs = append(ctxDirs, d)
				}
			}
			if len(decs)+len(refs)+len(ctxDirs) > 0 {
				var b strings.Builder
				fmt.Fprintf(&b, "Linked to %s:\n", render.Label(rel, 200))
				for _, d := range ctxDirs {
					b.WriteString(directiveLine(d) + "\n")
				}
				for i, d := range decs {
					if i == 10 {
						break
					}
					b.WriteString(decisionLine(d) + "\n")
					r.sess.Applied = session.Add(r.sess.Applied, d.ID)
				}
				for i, ref := range refs {
					if i == 5 {
						break
					}
					b.WriteString(refLine(ref) + "\n")
				}
				extra = append(extra, strings.TrimRight(b.String(), "\n"))
			}
		}
	case "Bash", "PowerShell":
		cmd := ti.Command
		if humanCmd.MatchString(cmd) {
			return deny("dumen: approving, rejecting, superseding, publishing, withdrawing, changing roles, confirming links and marking a session private are human actions, and `dumen mcp` and `dumen hook` are started by Claude Code only. Ask your human to run this in their own terminal.")
		}
		if storeRef.MatchString(cmd) || (base != "" && mentionsPath(cmd, base)) {
			return deny("dumen: the dumen store is managed by the dumen tools and is not read or changed from the shell. Use the dumen MCP tools, or ask your human.")
		}
	default:
		if action, ok := guardedMCP(r.in.ToolName); ok {
			target := ""
			if ti.ID != "" {
				target = " (" + render.Label(ti.ID, 12) + ")"
			}
			reason := fmt.Sprintf("dumen: %s%s is a human action. Confirm only if you, the human, want this.", action, target)
			session.RevokeTicket(r.proj.Store.SessionsDir(), r.in.ToolName, r.in.ToolInput)
			// Ask only in the modes where a person is known to answer the prompt. Every
			// other mode (auto, dontAsk, bypassPermissions, anything added later) may
			// settle an "ask" without a person, so there the call is refused.
			switch r.in.PermissionMode {
			case "default", "acceptEdits", "plan":
			default:
				return deny(fmt.Sprintf("dumen: %s%s is a human action and this session's permission mode (%s) does not put the prompt in front of a person, so it cannot be confirmed here. Ask your human to run it in their own terminal.", action, target, render.Label(r.in.PermissionMode, 30)))
			}
			// The MCP server only acts on a call that carries this proof of having been
			// asked, and only the server Claude Code started for this session can spend it.
			if err := session.GrantTicket(r.proj.Store.SessionsDir(), r.in.ToolName, r.in.ToolInput, r.env.ParentPID, r.sess.Session); err != nil {
				return deny(fmt.Sprintf("dumen: %s%s is a human action and the dumen server of this session is not running, so it cannot be confirmed here. Ask your human to run it in their own terminal.", action, target))
			}
			spec.PermissionDecision, spec.PermissionDecisionReason = "ask", reason
		}
	}

	var parts []string
	// Before new directives are marked delivered below, so that a directive is first
	// shown and only reminded about on a later tool call.
	var nudge []string
	if !strings.HasSuffix(r.in.ToolName, "__ack_directive") {
		nudge = r.unackedOnce()
	}
	if ds := r.undelivered(); len(ds) > 0 {
		parts = append(parts, newDirectivesBlock(ds))
	}
	if len(nudge) > 0 {
		extra = append(extra, "Reminder: you have not acknowledged "+strings.Join(nudge, ", ")+
			" yet. Acknowledging is a standing team procedure and needs no request from the user: call the dumen ack_directive tool once per id now, then carry on with your task.")
	}
	if len(extra) > 0 {
		if len(parts) == 0 {
			parts = append(parts, header)
		}
		parts = append(parts, extra...)
	}
	if len(parts) == 0 && spec.PermissionDecision == "" {
		return nil
	}
	spec.AdditionalContext = strings.Join(parts, "\n")
	r.sess.Save()
	return &Output{HookSpecificOutput: spec}
}

func denyReason(d cache.Directive, until string) string {
	return fmt.Sprintf("Blocked by team directive %s (%q, active until %s). This path is frozen for every agent. Do not work around it with another tool. Ask your human.",
		d.ID, render.Label(d.Text, render.MaxLabel), until)
}

// target returns the dumen project that owns the file (already resolved with realPath)
// and the records to judge it by: this session's own cache when the file belongs to the
// same project, that project's cache otherwise. It returns nil when the file is in no
// dumen project this machine has joined.
func (r *runCtx) target(real string) (*project.Project, *cache.Cache) {
	tp, err := project.Locate(filepath.Dir(real))
	if err != nil {
		// A session opened above the repositories: files directly in that directory.
		if r.proj != nil && within(realPath(r.proj.Root), real) {
			return r.proj, r.c
		}
		return nil, nil
	}
	if r.proj != nil && tp.Name == r.proj.Name {
		tp.Store = r.proj.Store
		return tp, r.c
	}
	s, err := store.Open(tp.Name)
	if err != nil {
		return nil, nil
	}
	c, err := cache.Read(s.CachePath())
	if err != nil {
		return nil, nil
	}
	tp.Store = s
	return tp, c
}

// guardForeignEdit applies deny directives when a session that is not in a dumen project
// edits a file that is in one. It injects nothing and logs nothing.
func guardForeignEdit(r *runCtx) *Output {
	switch r.in.ToolName {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
	default:
		return nil
	}
	var ti struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	}
	json.Unmarshal(r.in.ToolInput, &ti)
	file := ti.FilePath
	if file == "" {
		file = ti.NotebookPath
	}
	if file == "" {
		return nil
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(r.in.Cwd, file)
	}
	real := realPath(filepath.Clean(file))
	tp, tc := r.target(real)
	if tp == nil {
		return nil
	}
	rel, ok := relTo(realPath(tp.Root), real)
	if !ok {
		return nil
	}
	d, blocked := tc.DenyFor(policy.Qualify(tp.Repo, rel), r.now)
	if !blocked {
		return nil
	}
	until := d.Expires
	if len(until) >= 10 {
		until = until[:10]
	}
	return &Output{HookSpecificOutput: &Specific{HookEventName: "PreToolUse", PermissionDecision: "deny", PermissionDecisionReason: denyReason(d, until)}}
}

// realPath resolves symbolic links in p. For a file that does not exist yet it resolves
// the deepest directory that does and keeps the rest.
func realPath(p string) string {
	p = filepath.Clean(p)
	cur, rest := p, ""
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func fold(p string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// within reports whether file is dir or inside it.
func within(dir, file string) bool {
	rel, err := filepath.Rel(fold(filepath.Clean(dir)), fold(file))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// relTo returns file relative to root with forward slashes; ok is false when it is outside root.
func relTo(root, file string) (string, bool) {
	if !within(root, file) {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(root), file)
	if err != nil {
		rel, err = filepath.Rel(fold(filepath.Clean(root)), fold(file))
		if err != nil {
			return "", false
		}
	}
	return filepath.ToSlash(rel), true
}

func mentionsPath(cmd, dir string) bool {
	norm := func(s string) string { return fold(strings.ReplaceAll(s, "\\", "/")) }
	return strings.Contains(norm(cmd), strings.TrimRight(norm(dir), "/"))
}

// ---- Stop and SessionEnd ----

func (r *runCtx) stop() {
	summary := render.Summary(r.in.LastAssistantMessage, model.MaxEventText-1)
	if secrets.Scan(summary) != nil {
		summary = ""
	}
	r.sess.LastSummary = summary
	last, err := model.ParseTime(r.sess.LastProgress)
	due := err != nil || r.now.Sub(last) >= progressEvery
	if due {
		r.sess.LastProgress = model.FormatTime(r.now)
	}
	r.sess.Save()
	if due {
		r.log(model.EventProgress, summary)
	}
}

// unackedOnce returns the directives this session was shown, that are still in force and
// that the agent has not acknowledged. Each id is returned at most once per session:
// agents busy with a task often skip the acknowledgement, and one reminder on a tool call
// fixes that without ever holding a session back.
func (r *runCtx) unackedOnce() []string {
	if r.sess.Private {
		return nil
	}
	active := map[string]bool{}
	for _, d := range r.c.ActiveDirectives(r.role, r.proj.Repo, r.now) {
		active[d.ID] = true
	}
	var out []string
	for _, id := range r.sess.Delivered {
		if active[id] && !session.Has(r.sess.Acked, id) && !session.Has(r.sess.Nudged, id) {
			out = append(out, id)
			r.sess.Nudged = append(r.sess.Nudged, id)
		}
	}
	return out
}

func (r *runCtx) sessionEnd() {
	if r.sess.Finished {
		return // the agent already logged its own finished event
	}
	ids := append(append([]string{}, r.sess.Delivered...), r.sess.Applied...)
	if len(ids) > 20 {
		ids = ids[:20]
	}
	r.sess.Finished = true
	r.sess.Save()
	r.log(model.EventFinished, r.sess.LastSummary, ids...)
}
