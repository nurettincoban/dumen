package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nurettincoban/dumen/internal/hook"
	"github.com/nurettincoban/dumen/internal/project"
	"github.com/nurettincoban/dumen/internal/testutil"
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
	if out := run(t, "decide", "--title", "Orders are paginated with cursors", "--rule", "List endpoints page with opaque cursors", "--kind", "technical", "-m", "Decision. Why.", "--paths", "src/api", "--approve"); !strings.Contains(out, "approved") {
		t.Fatal(out)
	}
	if list := run(t, "decisions", "src/api/list.go"); !strings.Contains(list, "List endpoints page with opaque cursors") {
		t.Fatalf("decision not linked to the path in its own repository: %s", list)
	}
	// Decisions are scoped to services and topics and found by kind.
	if out, code := runErr("decide", "--title", "No kind", "--rule", "r", "-m", "b"); code == 0 || !strings.Contains(out, "--kind is required") {
		t.Fatalf("decide without --kind: %d %s", code, out)
	}
	run(t, "decide", "--title", "Payments go through billing", "--rule", "Only billing-svc talks to the payment provider",
		"--kind", "architecture", "--topics", "payments", "-m", "Why.", "--approve")
	run(t, "decide", "--title", "Orders keep idempotency keys", "--rule", "Creating an order is idempotent",
		"--kind", "product", "--services", "orders-svc", "--topics", "orders", "-m", "Why.", "--approve")
	for _, c := range []struct {
		args       []string
		want, deny string
	}{
		{[]string{"decisions", "--kind", "architecture"}, "Only billing-svc", "idempotent"},
		{[]string{"decisions", "--service", "billing-svc"}, "Only billing-svc", "idempotent"},
		{[]string{"decisions", "--service", "orders-svc"}, "idempotent", ""},
		{[]string{"decisions", "--topic", "orders"}, "idempotent", "Only billing-svc"},
	} {
		got := run(t, c.args...)
		if !strings.Contains(got, c.want) || (c.deny != "" && strings.Contains(got, c.deny)) {
			t.Fatalf("%v: %s", c.args, got)
		}
	}
	t.Chdir(billing)
	// Gauntlet G1: a decision limited by paths to one repository stays there in every view.
	run(t, "decide", "--title", "Refunds", "--rule", "Refunds are issued by the refund worker only", "--kind", "technical",
		"--paths", "src/**", "--topics", "payments", "-m", "Why.", "--approve")
	news := run(t, "news")
	if !strings.Contains(news, "refund worker") || !strings.Contains(news, "approved by "+testutil.Lead.Email+" in billing-svc") || strings.Contains(news, "idempotent") {
		t.Fatalf("news in billing-svc:\n%s", news)
	}
	if all := run(t, "news", "--all"); !strings.Contains(all, "idempotent") || !strings.Contains(all, "in orders-svc") {
		t.Fatalf("news for the project:\n%s", all)
	}
	if other := run(t, "news", "--repo", "orders-svc"); strings.Contains(other, "refund worker") {
		t.Fatalf("billing-svc's path decision in orders-svc news:\n%s", other)
	}
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
	if !strings.Contains(startO, "Product-wide:") || !strings.Contains(startO, "Only billing-svc talks") || strings.Contains(startB, "Creating an order is idempotent") {
		t.Errorf("product-wide decisions go to every service, service decisions only to theirs:\n%s\n%s", startB, startO)
	}
	if strings.Contains(startO, "billing auth is frozen") || !strings.Contains(startO, "structured logger") {
		t.Errorf("orders session must get the team-wide directive but not billing's freeze: %s", startO)
	}
	if strings.Contains(startB, "opaque cursors") || !strings.Contains(startO, "For orders-svc:") || !strings.Contains(startO, "[technical] List endpoints page with opaque cursors") {
		t.Errorf("decisions shown at session start must be the ones relevant to the repository:\n%s\n---\n%s", startB, startO)
	}
	// Frontend to backend: a decision in orders-svc that needs work from billing-svc
	// reaches a billing-svc session that is already running, with the ask.
	fire(billing, "sess-b-news", hook.SessionStart, map[string]any{"source": "startup"})
	t.Chdir(orders)
	run(t, "decide", "--title", "Show refund status", "--rule", "The order page shows the refund status", "--kind", "ui",
		"--services", "orders-svc", "--needs", "billing-svc=Expose refund status per order in GET /refunds", "-m", "Why.", "--approve")
	t.Chdir(billing)
	if got := fire(billing, "sess-b-news", hook.UserPromptSubmit, map[string]any{"prompt": "carry on"}); !strings.Contains(got, "DECISION NEWS") ||
		!strings.Contains(got, "NEEDS FROM billing-svc: Expose refund status per order") || !strings.Contains(got, "in orders-svc") {
		t.Errorf("billing-svc did not hear what orders-svc needs from it: %s", got)
	}
	prompt := map[string]any{"prompt": "Add partial refunds to the payments page"}
	if got := fire(orders, "sess-orders-2", hook.UserPromptSubmit, prompt); strings.Contains(got, "refund worker") {
		t.Errorf("a decision limited to billing-svc paths reached orders-svc: %s", got)
	}
	if got := fire(billing, "sess-billing-2", hook.UserPromptSubmit, map[string]any{"prompt": prompt["prompt"]}); !strings.Contains(got, "refund worker") {
		t.Errorf("the billing-svc decision was not noted in billing-svc: %s", got)
	}
	if !strings.Contains(startB, "For billing-svc:") || strings.Contains(startO, "refund worker") {
		t.Errorf("path-limited decision grouping:\n%s\n%s", startB, startO)
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
	if !strings.Contains(all, "started 3  finished 1  blocked 1") || !strings.Contains(all, "billing-svc 2") || !strings.Contains(all, "orders-svc 1") {
		t.Errorf("activity across services:\n%s", all)
	}
	if only := run(t, "activity", "--since", "today", "--repo", "orders-svc"); !strings.Contains(only, "started 1  finished 1  blocked 0") {
		t.Errorf("activity for one service:\n%s", only)
	}
}
