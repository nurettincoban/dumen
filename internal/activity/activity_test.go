package activity

import (
	"reflect"
	"testing"

	"github.com/nurettincoban/dumen/internal/model"
)

// A directive limited to one repository is only delivered to sessions working there, so
// people who never opened a session in it are not listed as "not yet acknowledged".
func TestUnackedCountsOnlyPeopleSeenInTheScopedRepository(t *testing.T) {
	cfg := model.Config{
		Roles: map[string]string{"maya@example.com": "pm", "ayse@example.com": "dev", "omar@example.com": "dev", "sofia@example.com": "dev"},
		Areas: map[string]model.Area{"auth": {Paths: []string{"billing-svc:src/auth/**"}}},
	}
	sum := Summary{
		Sessions: []Session{
			{Session: "a", Person: "ayse@example.com", Repo: "billing-svc"},
			{Session: "o", Person: "Omar@example.com", Repo: "billing-svc"},
			{Session: "s", Person: "sofia@example.com", Repo: "web-app"},
		},
		Acks: map[string][]string{"Y-AAAA": {"omar@example.com"}},
	}
	d := model.Directive{ID: "Y-AAAA", PublishedBy: "maya@example.com", Scope: model.Scope{Paths: []string{"billing-svc:src/auth/**"}}}
	if got, want := Unacked(cfg, d, sum), []string{"ayse@example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("repo-scoped paths: got %v, want %v", got, want)
	}
	d.Scope = model.Scope{Area: "auth"}
	if got, want := Unacked(cfg, d, sum), []string{"ayse@example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("repo-scoped area: got %v, want %v", got, want)
	}
	// A pattern without a repository can touch every repository: everyone is expected.
	for _, sc := range []model.Scope{{All: true}, {Paths: []string{"src/auth/**"}}, {Paths: []string{"billing-svc:src/**", "*:docs/**"}}} {
		d.Scope = sc
		if got, want := Unacked(cfg, d, sum), []string{"ayse@example.com", "sofia@example.com"}; !reflect.DeepEqual(got, want) {
			t.Errorf("scope %+v: got %v, want %v", sc, got, want)
		}
	}
}
