package api_test

import (
	"net/http"
	"testing"

	"github.com/vivianobiako/qless/api/internal/httpx"
)

type servingView struct {
	Serving *struct {
		ID        string  `json:"id"`
		Number    int     `json:"number"`
		StartedAt string  `json:"startedAt"`
		ServedAt  *string `json:"servedAt"`
	} `json:"serving"`
	Waiting []struct {
		ID string `json:"id"`
	} `json:"waiting"`
	Arrival struct {
		Sample int `json:"sample"`
	} `json:"arrival"`
}

// The moment service begins is inferred wherever it can be — already here
// when called, saying "here" at the counter, recalled from a skip — and is
// one tap otherwise. It never moves once set.
func TestServiceBeginsWhenTheCustomerIsActuallyThere(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Serving Shop")
	owner := header{"Authorization", "Bearer " + created.OwnerToken}
	slug := created.Queue.Slug

	amara, _ := client.join(slug, "Amara", "")
	kofi, _ := client.join(slug, "Kofi", "")
	client.join(slug, "Ngozi", "")

	// Amara says she is here while waiting; when called, service starts at
	// the call.
	client.do(http.MethodPost, "/api/queues/"+slug+"/presence",
		mustJSON(t, map[string]string{"presence": "HERE"}),
		header{httpx.CustomerTokenHeader, amara.CustomerToken})

	var view servingView
	res := client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	decode(t, res, &view)
	if view.Serving == nil || view.Serving.ServedAt == nil {
		t.Fatalf("expected service to begin at the call for somebody already here, got %+v", view.Serving)
	}

	// Kofi is called with nothing said: no service yet. He says "here" at
	// the counter, and that is the moment.
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	decode(t, res, &view)
	if view.Serving == nil || view.Serving.ServedAt != nil {
		t.Fatalf("expected no service start for somebody not yet here, got %+v", view.Serving)
	}
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/presence",
		mustJSON(t, map[string]string{"presence": "HERE"}),
		header{httpx.CustomerTokenHeader, kofi.CustomerToken})
	if res.status != http.StatusOK {
		t.Fatalf("presence: %d %s", res.status, res.body)
	}
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil, owner)
	decode(t, res, &view)
	if view.Serving == nil || view.Serving.ServedAt == nil {
		t.Fatalf("expected 'here' at the counter to begin service, got %+v", view.Serving)
	}

	// Ngozi is called and walks up without her phone: one tap. A second tap
	// changes nothing, and tapping for somebody waiting is refused.
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	decode(t, res, &view)
	ngozi := view.Serving.ID
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/entries/"+ngozi+"/start", nil, owner)
	if res.status != http.StatusOK {
		t.Fatalf("start: %d %s", res.status, res.body)
	}
	decode(t, res, &view)
	if view.Serving.ServedAt == nil {
		t.Fatal("expected the tap to begin service")
	}
	first := *view.Serving.ServedAt
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/entries/"+ngozi+"/start", nil, owner)
	decode(t, res, &view)
	if view.Serving.ServedAt == nil || *view.Serving.ServedAt != first {
		t.Fatalf("a second tap must not move the moment, got %v then %v", first, view.Serving.ServedAt)
	}

	client.join(slug, "Late", "")
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil, owner)
	decode(t, res, &view)
	res = client.do(http.MethodPost, "/api/queues/"+slug+"/entries/"+view.Waiting[0].ID+"/start", nil, owner)
	if res.status != http.StatusConflict {
		t.Fatalf("expected 409 for starting somebody who is waiting, got %d %s", res.status, res.body)
	}

	// Finishing the three served so far leaves three arrival measurements.
	client.do(http.MethodPost, "/api/queues/"+slug+"/next", nil, owner)
	res = client.do(http.MethodGet, "/api/queues/"+slug+"/entries", nil, owner)
	decode(t, res, &view)
	if view.Arrival.Sample != 3 {
		t.Fatalf("expected three arrival measurements, got %d", view.Arrival.Sample)
	}
}
