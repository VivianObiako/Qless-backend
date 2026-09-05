package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/push"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
	"github.com/vivianobiako/qless/api/internal/token"
)

// The ladder, numbered so a phone is only ever told about a rise. It mirrors
// proximityOf in the web app: your turn, next, close, and nothing.
const (
	rungWaiting = 0
	rungClose   = 1
	rungNext    = 2
	rungCurrent = 3
)

// closeWithin is how many people ahead counts as "getting close".
const closeWithin = 3

// WithPush turns the nudges on. webOrigin is where a tapped notification
// lands, so it is the web app's own address rather than this API's.
func (s *Server) WithPush(sender *push.Sender, webOrigin string) *Server {
	s.push = sender
	s.webOrigin = strings.TrimRight(webOrigin, "/")
	return s
}

type pushKeyResponse struct {
	PublicKey string `json:"publicKey"`
}

// pushKey hands the browser the public half of the VAPID pair, or says push
// is not set up here — which the pass treats as "keep the in-page nudge".
func (s *Server) pushKey(w http.ResponseWriter, r *http.Request) {
	if !s.push.Enabled() {
		httpx.WriteError(w, http.StatusNotFound, "push_unavailable", "Push notifications aren't set up on this server.")
		return
	}
	httpx.JSON(w, http.StatusOK, pushKeyResponse{PublicKey: s.push.PublicKey()})
}

type pushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// subscribePush binds a browser subscription to the caller's active entry.
func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	if !s.push.Enabled() {
		httpx.WriteError(w, http.StatusNotFound, "push_unavailable", "Push notifications aren't set up on this server.")
		return
	}

	q, ok := s.resolveQueue(w, r)
	if !ok {
		return
	}
	raw := strings.TrimSpace(r.Header.Get(httpx.CustomerTokenHeader))
	if raw == "" {
		writeError(w, queue.ErrNotInQueue)
		return
	}

	var req pushSubscribeRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") ||
		req.Keys.P256dh == "" || req.Keys.Auth == "" {
		writeError(w, invalid("That doesn't look like a push subscription."))
		return
	}

	err := s.store.SavePushSubscription(r.Context(), q.ID, token.Hash(raw), push.Subscription{
		Endpoint: endpoint,
		P256dh:   req.Keys.P256dh,
		Auth:     req.Keys.Auth,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pushUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}

// unsubscribePush forgets an endpoint. No token needed: an endpoint is a
// secret only its browser holds, and forgetting one is never harmful.
func (s *Server) unsubscribePush(w http.ResponseWriter, r *http.Request) {
	var req pushUnsubscribeRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}
	if err := s.store.DeletePushSubscription(r.Context(), strings.TrimSpace(req.Endpoint)); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// notifyPush tells every subscribed phone in the queue that has moved up the
// ladder since it was last told. It runs after the frame has gone out, on
// its own clock, so a slow push service never delays the counter.
func (s *Server) notifyPush(queueID string) {
	if !s.push.Enabled() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	q, err := s.store.GetQueue(ctx, queueID)
	if err != nil {
		slog.Error("push: read queue", "error", err, "queue", queueID)
		return
	}
	state, err := s.store.PublicState(ctx, q)
	if err != nil {
		slog.Error("push: read state", "error", err, "queue", queueID)
		return
	}
	targets, err := s.store.PushTargets(ctx, queueID)
	if err != nil {
		slog.Error("push: list targets", "error", err, "queue", queueID)
		return
	}

	for _, target := range targets {
		rung, ahead := rungFor(target, state)
		if rung <= target.LastRung {
			continue
		}

		msg := messageFor(rung, target.Number, ahead, q, s.webOrigin)
		err := s.push.Send(ctx, target.Subscription, msg)
		switch {
		case errors.Is(err, push.ErrGone):
			if err := s.store.DeletePushSubscriptionByID(ctx, target.ID); err != nil {
				slog.Error("push: forget subscription", "error", err)
			}
			continue
		case err != nil:
			slog.Warn("push: send", "error", err, "queue", queueID, "number", target.Number)
			continue
		}
		if err := s.store.MarkPushRung(ctx, target.ID, rung); err != nil {
			slog.Error("push: mark rung", "error", err)
		}
	}
}

// rungFor is proximityOf, on the server.
func rungFor(target storage.PushTarget, state queue.PublicState) (rung, ahead int) {
	if target.Status == queue.EntryServing {
		return rungCurrent, 0
	}
	ahead = state.PeopleAhead(target.Number)
	switch {
	case ahead == 0:
		return rungNext, ahead
	case ahead <= closeWithin:
		return rungClose, ahead
	default:
		return rungWaiting, ahead
	}
}

// messageFor says the same thing the pass would have, in the same words.
func messageFor(rung, number, ahead int, q queue.Queue, webOrigin string) push.Message {
	msg := push.Message{
		URL: fmt.Sprintf("%s/q/%s", webOrigin, q.Slug),
		// One notification per queue: getting close and then being called
		// replace each other rather than stacking.
		Tag: "qless-" + q.Slug,
	}
	switch rung {
	case rungCurrent:
		msg.Title = "It's your turn"
		msg.Body = fmt.Sprintf("#%d at %s. Head to the counter.", number, q.Name)
	case rungNext:
		msg.Title = "You're next"
		msg.Body = fmt.Sprintf("#%d at %s. Be inside now.", number, q.Name)
	default:
		people := fmt.Sprintf("%d people", ahead)
		if ahead == 1 {
			people = "One person"
		}
		msg.Title = "You're getting close"
		msg.Body = fmt.Sprintf("#%d at %s. %s ahead — start heading back.", number, q.Name, people)
	}
	return msg
}
