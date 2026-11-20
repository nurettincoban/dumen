// Package store reads and writes dumen records in a checkout of the dumen branch.
package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nurettincoban/comm-pro/internal/model"
)

// Names of files and directories on the dumen branch.
const (
	ConfigFile    = "dumen.yml"
	DecisionsDir  = "decisions"
	DirectivesDir = "directives"
	ActivityDir   = "activity"
	RefsDir       = "refs"
	ClaimsDir     = "claims"
	ArchiveDir    = "archive" // decisions/archive: auto-archived drafts
	DecisionsMD   = "DECISIONS.md"
	ActivityMD    = "ACTIVITY.md"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

func marshalYAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// frontMatter renders "---\n<yaml>---\n\n<body>\n".
func frontMatter(meta any, body string) ([]byte, error) {
	y, err := marshalYAML(meta)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(y)
	buf.WriteString("---\n")
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body != "" {
		buf.WriteString("\n")
		buf.WriteString(body)
		buf.WriteString("\n")
	}
	return buf.Bytes(), nil
}

func parseFrontMatter(data []byte, meta any) (string, error) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return "", fmt.Errorf("missing front matter")
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			end = len(rest) - 4
			rest += "\n"
		} else {
			return "", fmt.Errorf("unterminated front matter")
		}
	}
	if err := yaml.Unmarshal([]byte(rest[:end+1]), meta); err != nil {
		return "", err
	}
	return strings.TrimSpace(rest[end+5:]), nil
}

// ---- config ----

// ReadConfig loads dumen.yml from root.
func ReadConfig(root string) (model.Config, error) {
	var c model.Config
	data, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %v", ConfigFile, err)
	}
	if c.Roles == nil {
		c.Roles = map[string]string{}
	}
	norm := make(map[string]string, len(c.Roles))
	for k, v := range c.Roles {
		norm[strings.ToLower(strings.TrimSpace(k))] = v
	}
	c.Roles = norm
	def := model.DefaultConfig(c.Project).Settings
	if len(c.Settings.Publishers) == 0 {
		c.Settings.Publishers = def.Publishers
	}
	if c.Settings.MaxActiveDirectives == 0 {
		c.Settings.MaxActiveDirectives = def.MaxActiveDirectives
	}
	if c.Settings.DraftArchiveDays == 0 {
		c.Settings.DraftArchiveDays = def.DraftArchiveDays
	}
	if c.Settings.RefStaleDays == 0 {
		c.Settings.RefStaleDays = def.RefStaleDays
	}
	return c, nil
}

// WriteConfig writes dumen.yml to root.
func WriteConfig(root string, c model.Config) error {
	y, err := marshalYAML(c)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(root, ConfigFile), y)
}

// ---- generic record lookup ----

// findByID returns the path (relative to root) of the record file for id in dir.
// Record files are named <id>.<ext> or <id>-<slug>.<ext>.
func findByID(root, dir, id, ext string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ext) {
			continue
		}
		base := strings.TrimSuffix(n, ext)
		if base == id || strings.HasPrefix(base, id+"-") {
			return dir + "/" + n, nil
		}
	}
	return "", ErrNotFound
}

func listFiles(root, dir, ext string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			out = append(out, dir+"/"+e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func recordName(id, slug, ext string) string {
	if slug == "" {
		return id + ext
	}
	return id + "-" + slug + ext
}

// Exists reports whether a record with this ID exists (decisions also count when archived).
func Exists(root, id string) bool {
	switch {
	case strings.HasPrefix(id, model.PrefixDecision+"-"):
		if _, err := findByID(root, DecisionsDir, id, ".md"); err == nil {
			return true
		}
		_, err := findByID(root, DecisionsDir+"/"+ArchiveDir, id, ".md")
		return err == nil
	case strings.HasPrefix(id, model.PrefixDirective+"-"):
		_, err := findByID(root, DirectivesDir, id, ".md")
		return err == nil
	case strings.HasPrefix(id, model.PrefixRef+"-"):
		_, err := findByID(root, RefsDir, id, ".yml")
		return err == nil
	}
	return false
}

// ---- decisions ----

func readDecisionFile(root, rel string) (model.Decision, error) {
	var d model.Decision
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return d, err
	}
	body, err := parseFrontMatter(data, &d)
	if err != nil {
		return d, fmt.Errorf("%s: %v", rel, err)
	}
	d.Body = body
	return d, nil
}

// ReadDecision loads a decision by ID. It returns the file path relative to root.
func ReadDecision(root, id string) (model.Decision, string, error) {
	rel, err := findByID(root, DecisionsDir, id, ".md")
	if err != nil {
		return model.Decision{}, "", fmt.Errorf("decision %s: %w", id, err)
	}
	d, err := readDecisionFile(root, rel)
	return d, rel, err
}

// WriteDecision writes a decision. An existing file for the same ID is replaced in place,
// so the file name stays stable even if the title changes.
func WriteDecision(root string, d model.Decision) (string, error) {
	rel, err := findByID(root, DecisionsDir, d.ID, ".md")
	if err != nil {
		rel = DecisionsDir + "/" + recordName(d.ID, model.Slug(d.Title), ".md")
	}
	data, err := frontMatter(d, d.Body)
	if err != nil {
		return "", err
	}
	return rel, writeFile(filepath.Join(root, filepath.FromSlash(rel)), data)
}

// ListDecisions returns all non-archived decisions sorted by ID.
func ListDecisions(root string) ([]model.Decision, error) {
	files, err := listFiles(root, DecisionsDir, ".md")
	if err != nil {
		return nil, err
	}
	out := make([]model.Decision, 0, len(files))
	for _, f := range files {
		d, err := readDecisionFile(root, f)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ArchiveDecision moves a decision file into decisions/archive.
func ArchiveDecision(root, id string) error {
	rel, err := findByID(root, DecisionsDir, id, ".md")
	if err != nil {
		return err
	}
	dst := filepath.Join(root, DecisionsDir, ArchiveDir, filepath.Base(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(filepath.Join(root, filepath.FromSlash(rel)), dst)
}

// ---- directives ----

func readDirectiveFile(root, rel string) (model.Directive, error) {
	var d model.Directive
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return d, err
	}
	body, err := parseFrontMatter(data, &d)
	if err != nil {
		return d, fmt.Errorf("%s: %v", rel, err)
	}
	d.Text = body
	return d, nil
}

// ReadDirective loads a directive by ID.
func ReadDirective(root, id string) (model.Directive, string, error) {
	rel, err := findByID(root, DirectivesDir, id, ".md")
	if err != nil {
		return model.Directive{}, "", fmt.Errorf("directive %s: %w", id, err)
	}
	d, err := readDirectiveFile(root, rel)
	return d, rel, err
}

// WriteDirective writes a directive, replacing an existing file for the same ID.
func WriteDirective(root string, d model.Directive) (string, error) {
	rel, err := findByID(root, DirectivesDir, d.ID, ".md")
	if err != nil {
		rel = DirectivesDir + "/" + recordName(d.ID, model.Slug(d.Text), ".md")
	}
	data, err := frontMatter(d, d.Text)
	if err != nil {
		return "", err
	}
	return rel, writeFile(filepath.Join(root, filepath.FromSlash(rel)), data)
}

// ListDirectives returns all directives sorted by ID.
func ListDirectives(root string) ([]model.Directive, error) {
	files, err := listFiles(root, DirectivesDir, ".md")
	if err != nil {
		return nil, err
	}
	out := make([]model.Directive, 0, len(files))
	for _, f := range files {
		d, err := readDirectiveFile(root, f)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ---- refs ----

// ReadRef loads a ref by ID.
func ReadRef(root, id string) (model.Ref, error) {
	var r model.Ref
	rel, err := findByID(root, RefsDir, id, ".yml")
	if err != nil {
		return r, fmt.Errorf("ref %s: %w", id, err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return r, err
	}
	if err := yaml.Unmarshal(data, &r); err != nil {
		return r, fmt.Errorf("%s: %v", rel, err)
	}
	return r, nil
}

// WriteRef writes refs/<id>.yml.
func WriteRef(root string, r model.Ref) (string, error) {
	y, err := marshalYAML(r)
	if err != nil {
		return "", err
	}
	rel := RefsDir + "/" + r.ID + ".yml"
	return rel, writeFile(filepath.Join(root, filepath.FromSlash(rel)), y)
}

// ListRefs returns all refs sorted by ID.
func ListRefs(root string) ([]model.Ref, error) {
	files, err := listFiles(root, RefsDir, ".yml")
	if err != nil {
		return nil, err
	}
	out := make([]model.Ref, 0, len(files))
	for _, f := range files {
		var r model.Ref
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ---- activity ----

// EventPath returns the activity file for an event: activity/YYYY-MM-DD/<session>.jsonl (UTC day).
func EventPath(e model.Event) string {
	day := e.TS
	if len(day) >= 10 {
		day = day[:10]
	}
	return ActivityDir + "/" + day + "/" + e.Session + ".jsonl"
}

// AppendEvent appends one event line to its session file.
func AppendEvent(root string, e model.Event) (string, error) {
	rel := EventPath(e)
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return "", err
	}
	return rel, f.Close()
}

// ListDays returns the activity days present, sorted ascending. A day is either a
// directory of per-session files or, after compaction, a single YYYY-MM-DD.jsonl file.
func ListDays(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, ActivityDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			name = strings.TrimSuffix(name, ".jsonl")
		}
		if len(name) == 10 {
			seen[name] = true
		}
	}
	days := make([]string, 0, len(seen))
	for d := range seen {
		days = append(days, d)
	}
	sort.Strings(days)
	return days, nil
}

func readEventFile(path string, out []model.Event) ([]model.Event, error) {
	fh, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e model.Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // a damaged line must not hide the rest of the log
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// ReadEvents returns the events of one day, ordered by (ts, session, position in file).
func ReadEvents(root, day string) ([]model.Event, error) {
	out, err := readEventFile(filepath.Join(root, ActivityDir, day+".jsonl"), nil)
	if err != nil {
		return nil, err
	}
	files, err := listFiles(root, ActivityDir+"/"+day, ".jsonl")
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if out, err = readEventFile(filepath.Join(root, filepath.FromSlash(f)), out); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS < out[j].TS
		}
		return out[i].Session < out[j].Session
	})
	return out, nil
}

// CompactDay merges a day's per-session files into activity/<day>.jsonl and removes the
// directory. It reports whether anything changed.
func CompactDay(root, day string) (bool, error) {
	dir := filepath.Join(root, ActivityDir, day)
	if _, err := os.Stat(dir); err != nil {
		return false, nil
	}
	evs, err := ReadEvents(root, day)
	if err != nil {
		return false, err
	}
	var buf bytes.Buffer
	for _, e := range evs {
		line, err := json.Marshal(e)
		if err != nil {
			return false, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := writeFile(filepath.Join(root, ActivityDir, day+".jsonl"), buf.Bytes()); err != nil {
		return false, err
	}
	return true, os.RemoveAll(dir)
}
