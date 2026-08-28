package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/realtime"
	"github.com/vivianobiako/qless/api/internal/token"
)

// queueSocket subscribes a browser to one queue's changes.
//
// The audience is settled here, before the upgrade, and the connection can
// never change it afterwards: without a valid owner token this socket will only
// ever be handed public frames, whatever the browser asks for.
func (s *Server) queueSocket(w http.ResponseWriter, r *http.Request) {
	q, ok := s.resolveQueue(w, r)
	if !ok {
		return
	}

	if !s.socketLimiter.Allow(httpx.ClientIP(r)) {
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "Too many connection attempts. Try again in a minute.")
		return
	}

	audience := realtime.Public

	// A browser cannot set an Authorization header on a WebSocket handshake, so
	// the operator's token arrives in the query string — the same place the
	// dashboard URL already carries it. A token that is present but wrong is
	// refused rather than quietly downgraded: a dashboard showing a live
	// indicator over a feed that will never contain names is worse than an
	// error the operator can act on.
	if raw := strings.TrimSpace(r.URL.Query().Get("k")); raw != "" {
		actor, err := s.store.ResolveActor(r.Context(), token.Hash(raw))
		if err != nil {
			writeError(w, err)
			return
		}
		if err := s.store.AuthorizeQueue(r.Context(), actor, q.ID); err != nil {
			writeError(w, err)
			return
		}

		// Which dashboard frame this connection gets is decided once, here,
		// from who they turned out to be — not from anything they send later.
		audience = realtime.Staff
		if actor.IsOwner() {
			audience = realtime.Owner
		}
	}

	s.sockets.Serve(w, r, q.ID, audience, func() []byte {
		event, err := s.buildEvent(r.Context(), q.ID, EventQueueUpdated)
		if err != nil {
			slog.Error("build initial socket snapshot", "error", err, "queue", q.ID)
			return nil
		}
		return event.FrameFor(audience)
	})
}
