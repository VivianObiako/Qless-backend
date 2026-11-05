package api_test

import (
	"net/http"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

// On a team-name queue a name already holding a place is refused, however it
// is capitalised or spaced, from a phone or from the counter. Once that team
// is finished with, the name is free again.
func TestTeamNameQueueRefusesADuplicateTeam(t *testing.T) {
	op := newOperator(t, "Code Clash")
	slug := op.queue.Queue.Slug

	if _, res := op.client.join(slug, "Team Alpha", ""); res.status != http.StatusCreated {
		t.Fatalf("before the setting, join: %d %s", res.status, res.body)
	}
	if _, res := op.client.join(slug, "Team Alpha", ""); res.status != http.StatusCreated {
		t.Fatalf("an ordinary queue should allow two of the same name: %d %s", res.status, res.body)
	}

	op.mustDo(http.MethodPatch, "", []byte(`{"asksTeamName":true}`))
	var public struct {
		State struct {
			Queue struct {
				AsksTeamName bool `json:"asksTeamName"`
			} `json:"queue"`
		} `json:"state"`
	}
	decode(t, op.client.do(http.MethodGet, "/api/queues/"+slug, nil), &public)
	if !public.State.Queue.AsksTeamName {
		t.Fatal("the public summary does not say the queue asks for a team name")
	}

	res := op.client.do(http.MethodPost, "/api/queues/"+slug+"/join", []byte(`{"name":"  team   ALPHA "}`))
	if res.status != http.StatusConflict {
		t.Fatalf("duplicate team: status %d, want 409; body %s", res.status, res.body)
	}
	var body httpx.ErrorBody
	decode(t, res, &body)
	if body.Error != "team_name_taken" {
		t.Errorf("error code = %q, want team_name_taken", body.Error)
	}

	if res := op.do(http.MethodPost, "/entries", []byte(`{"name":"team alpha"}`)); res.status != http.StatusConflict {
		t.Fatalf("duplicate walk-in team: status %d, want 409; body %s", res.status, res.body)
	}

	if _, res := op.client.join(slug, "Team Bravo", ""); res.status != http.StatusCreated {
		t.Fatalf("a different team: %d %s", res.status, res.body)
	}

	// Finish with both Team Alphas, and the name is free again.
	for _, entry := range op.view().Waiting {
		if entry.Name == "Team Alpha" {
			op.mustDo(http.MethodPost, "/entries/"+entry.ID+"/remove", nil)
		}
	}
	if _, res := op.client.join(slug, "Team Alpha", ""); res.status != http.StatusCreated {
		t.Fatalf("rejoining after the team was finished with: %d %s", res.status, res.body)
	}
}
