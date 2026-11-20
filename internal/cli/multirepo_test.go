package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurettincoban/comm-pro/internal/hook"
	"github.com/nurettincoban/comm-pro/internal/project"
	"github.com/nurettincoban/comm-pro/internal/testutil"
)

// Two service repositories share one store repository. A freeze on one service must not
// touch the other, and the activity view must tell the services apart.
func TestSeveralRepositoriesShareOneStore(t *testing.T) {
	testutil.Home(t)
	t.Setenv("CLAUDECODE", "")
	storeRemote := testutil.BareRemote(t) // the dedicated store repository
	mkRepo := func(name string) string {
		dir := filepath.Join(t.TempDir(), name)
		for _, args := range [][]string{
			{"init", "-q", dir}, {"-C", dir, "config", "user.email", testutil.Lead.Email}, {"-C", dir, "config", "user.name", "Lead"},
			{"-C", dir, "remote", "add", "origin", "https://git.example.com/team/" + name + ".git"},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		return dir
	}
	billing, orders := mkRepo("billing-svc"), mkRepo("orders-svc")

	t.Chdir(billing)
	out := run(t, "init", "--store", storeRemote+"#dumen", "--project", "team")
	if !strings.Contains(out, `"billing-svc" in project "team"`) {
		t.Fatalf("init in the first service: %s", out)
	}
	t.Chdir(orders)
	run(t, "init", "--store", storeRemote+"#dumen", "--project", "team")
	for dir, want := range map[string]string{billing: "billing-svc", orders: "orders-svc"} {
		p, err := project.Find(dir)
		if err != nil || p.Repo != want || p.Name != "team" {
			t.Fatalf("project file in %s: %+v %v", dir, p, err)
		}
	}

	run(t, "direct", "--deny", "--paths", "billing-svc:src/auth/**", "Release week: billing auth is frozen")
	run(t, "direct", "--all", "All services log with the structured logger")
	// A decision recorded inside a repository belongs to that repository.
	if out := run(t, "decide", "--title", "Orders are paginated with cursors", "-m", "Decision. Why.", "--paths", "src/api", "--approve"); !strings.Contains(out, "approved") {
		t.Fatal(out)
	}
	if list := run(t, "decisions", "src/api/list.go"); !strings.Contains(list, "Orders are paginated") {
		t.Fatalf("decision not linked to the path in its own repository: %s", list)
	}
	t.Chdir(billing)
	if list := run(t, "decisions", "src/api/list.go"); strings.Contains(list, "Orders are paginated") {
		t.Fatalf("a decision about orders-svc leaked into billing-svc: %s", list)
	}

	fire := func(dir, session, event string, in map[string]any) string {
		in["session_id"], in["cwd"] = session, dir
		data, _ := json.Marshal(in)
		var buf bytes.Buffer
		hook.Run(event, bytes.NewReader(data), &buf, hook.Env{})
		return buf.String()
	}
	edit := func(dir, session string) string {
		return fire(dir, session, hook.PreToolUse, map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(dir, "src", "auth", "login.go")}})
	}
	startB := fire(billing, "sess-billing", hook.SessionStart, map[string]any{"source": "startup"})
	startO := fire(orders, "sess-orders", hook.SessionStart, map[string]any{"source": "startup"})
	if !strings.Contains(startB, "billing auth is frozen") || !strings.Contains(startB, "This repository: billing-svc") {
		t.Errorf("billing session start: %s", startB)
	}
	if strings.Contains(startO, "billing auth is frozen") || !strings.Contains(startO, "structured logger") {
		t.Errorf("orders session must get the team-wide directive but not billing's freeze: %s", startO)
	}
	if strings.Contains(startB, "Orders are paginated") || !strings.Contains(startO, "Orders are paginated") {
		t.Errorf("decisions shown at session start must be the ones relevant to the repository")
	}
	if got := edit(billing, "sess-billing"); !strings.Contains(got, `"deny"`) {
		t.Errorf("edit in the frozen service must be denied: %s", got)
	}
	if got := edit(orders, "sess-orders"); strings.Contains(got, `"deny"`) {
		t.Errorf("the same path in another service must not be denied: %s", got)
	}
	fire(orders, "sess-orders", hook.SessionEnd, map[string]any{"reason": "other"})

	p, _ := project.Find(billing)
	if rep, err := p.Engine().Flush(context.Background()); err != nil || rep.Offline {
		t.Fatalf("flush: %+v %v", rep, err)
	}
	all := run(t, "activity", "--since", "today")
	if !strings.Contains(all, "started 2  finished 1  blocked 1") || !strings.Contains(all, "billing-svc 1") || !strings.Contains(all, "orders-svc 1") {
		t.Errorf("activity across services:\n%s", all)
	}
	if only := run(t, "activity", "--since", "today", "--repo", "orders-svc"); !strings.Contains(only, "started 1  finished 1  blocked 0") {
		t.Errorf("activity for one service:\n%s", only)
	}
}
