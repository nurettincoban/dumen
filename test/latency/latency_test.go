// Package latency holds acceptance test 4: a hook invocation that reads the local cache
// must finish in under 50 ms at the 95th percentile, measured on the real binary.
package latency

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/nurettincoban/dumen/internal/model"
	"github.com/nurettincoban/dumen/internal/op"
	"github.com/nurettincoban/dumen/internal/project"
	"github.com/nurettincoban/dumen/internal/testutil"
)

const (
	runs   = 200
	budget = 50 * time.Millisecond
)

func TestHookLatencyP95(t *testing.T) {
	home := testutil.Home(t)
	remote := testutil.BareRemote(t)
	s := testutil.NewStore(t, remote, "proj")
	root := t.TempDir()
	if err := project.WriteFile(root, "proj", remote+"#dumen"); err != nil {
		t.Fatal(err)
	}
	p := &project.Project{Root: root, Name: "proj", Store: s}
	eng := p.Engine()

	// A store larger than a six-person team is likely to have: 10 directives, 300 decisions, 60 refs.
	expires := model.FormatTime(time.Now().Add(72 * time.Hour))
	for i := 0; i < 10; i++ {
		d := model.Directive{ID: model.NewID("Y"), Kind: model.DirectiveContext, Scope: model.Scope{All: true}, Expires: expires}
		if i%2 == 0 {
			d.Kind, d.Scope = model.DirectiveDeny, model.Scope{Paths: []string{fmt.Sprintf("src/frozen%d/**", i)}}
		}
		if _, err := eng.Enqueue(testutil.Lead, &op.CreateDirective{Directive: d, Text: fmt.Sprintf("Directive number %d with a realistic amount of text in it", i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 300; i++ {
		d := model.Decision{ID: model.NewID("D"), Title: fmt.Sprintf("Decision %d about module %d", i, i%40), Paths: []string{fmt.Sprintf("src/mod%d", i%40)}}
		if _, err := eng.Enqueue(testutil.Lead, &op.CreateDecision{Decision: d, Body: "Decision.\n\nWhy.\n\nRejected alternatives.", Approve: true}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 60; i++ {
		r := model.Ref{ID: model.NewID("R"), URL: fmt.Sprintf("https://example.com/design/%d", i), Kind: "design", Title: fmt.Sprintf("Design %d", i), Paths: []string{fmt.Sprintf("src/mod%d", i%40)}}
		if _, err := eng.Enqueue(testutil.Lead, &op.AddRef{Ref: r}); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := eng.Flush(context.Background())
	if err != nil || rep.Offline || rep.Contended {
		t.Fatalf("flush: %+v %v", rep, err)
	}
	for _, o := range rep.Outcomes {
		if o.Status != op.Applied {
			t.Fatalf("setup operation not applied: %+v", o)
		}
	}

	bin := filepath.Join(t.TempDir(), "dumen")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/nurettincoban/dumen/cmd/dumen").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := append(os.Environ(), "DUMEN_HOME="+home, "CLAUDE_PROJECT_DIR="+root)

	invoke := func(event string, in map[string]any) (time.Duration, []byte) {
		in["session_id"], in["cwd"], in["permission_mode"] = "bench", root, "default"
		data, _ := json.Marshal(in)
		cmd := exec.Command(bin, "hook", event)
		cmd.Env = env
		cmd.Stdin = bytes.NewReader(data)
		var out bytes.Buffer
		cmd.Stdout = &out
		start := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatalf("hook %s: %v", event, err)
		}
		return time.Since(start), out.Bytes()
	}
	p95 := func(name string, f func(i int) time.Duration) {
		d := make([]time.Duration, runs)
		for i := range d {
			d[i] = f(i)
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		got := d[runs*95/100]
		t.Logf("%-28s p50 %v  p95 %v  max %v", name, d[runs/2], got, d[runs-1])
		if got > budget {
			t.Errorf("%s: p95 %v is over the %v budget", name, got, budget)
		}
	}

	if _, out := invoke("session-start", map[string]any{"source": "startup"}); !bytes.Contains(out, []byte("ACTIVE DIRECTIVES (10)")) {
		t.Fatalf("session-start did not inject the directives: %s", out)
	}
	if _, out := invoke("pre-tool-use", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(root, "src", "frozen0", "a.go")}}); !bytes.Contains(out, []byte(`"deny"`)) {
		t.Fatalf("deny not enforced by the binary: %s", out)
	}
	p95("pre-tool-use (edit, new path)", func(i int) time.Duration {
		d, _ := invoke("pre-tool-use", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(root, "src", fmt.Sprintf("mod%d", i%40), fmt.Sprintf("f%d.go", i))}})
		return d
	})
	p95("pre-tool-use (other tool)", func(i int) time.Duration {
		d, _ := invoke("pre-tool-use", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "/x"}})
		return d
	})
	p95("user-prompt-submit", func(i int) time.Duration {
		d, _ := invoke("user-prompt-submit", map[string]any{"prompt": "hello"})
		return d
	})
	p95("session-start", func(i int) time.Duration {
		d, _ := invoke("session-start", map[string]any{"source": "compact"})
		return d
	})
}
