// Package outbox is the local queue of operations waiting to reach the remote.
// Every write goes through it, so online and offline writes share one code path.
package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
)

// Entry is one queued operation.
type Entry struct {
	ID      string          `json:"id"`   // operation id, also written as a commit trailer
	Kind    string          `json:"kind"` // operation kind, selects the decoder
	Actor   model.Actor     `json:"actor"`
	Created string          `json:"created"`
	Payload json.RawMessage `json:"payload"`
	// MaybePushed is set before a push and cleared after a clean rejection. If it is still
	// set on the next flush, the previous push may have landed and history is checked first.
	MaybePushed bool   `json:"maybe_pushed,omitempty"`
	Error       string `json:"error,omitempty"` // set when moved to the failed directory

	path string
}

// Add writes a new entry to dir. The file name sorts by creation time.
func Add(dir string, e *Entry) error {
	if e.ID == "" {
		e.ID = model.NewOpID()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	e.path = filepath.Join(dir, fmt.Sprintf("%020d-%s.json", time.Now().UnixNano(), e.ID))
	return e.Save()
}

// Save rewrites the entry atomically.
func (e *Entry) Save() error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

// Remove deletes the entry from the queue.
func (e *Entry) Remove() error {
	err := os.Remove(e.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Fail moves the entry to failedDir, recording why it was rejected.
func (e *Entry) Fail(failedDir string, reason error) error {
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		return err
	}
	old := e.path
	e.Error = reason.Error()
	e.path = filepath.Join(failedDir, filepath.Base(old))
	if err := e.Save(); err != nil {
		return err
	}
	return os.Remove(old)
}

// List returns the entries in dir, oldest first.
func List(dir string) ([]*Entry, error) {
	names, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, n := range names {
		if !n.IsDir() && strings.HasSuffix(n.Name(), ".json") {
			files = append(files, n.Name())
		}
	}
	sort.Strings(files)
	out := make([]*Entry, 0, len(files))
	for _, f := range files {
		p := filepath.Join(dir, f)
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed by a concurrent flush
			}
			return nil, err
		}
		e := &Entry{}
		if err := json.Unmarshal(data, e); err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		e.path = p
		out = append(out, e)
	}
	return out, nil
}

// Count returns the number of queued entries.
func Count(dir string) int {
	l, _ := List(dir)
	return len(l)
}
