package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vivianobiako/qless/api/internal/httpx"
	"github.com/vivianobiako/qless/api/internal/queue"
	"github.com/vivianobiako/qless/api/internal/storage"
)

// Seats are the places a queue serves people at. Reading them is shared
// with operators, who need them to pick a chair; changing them — name,
// order, open or closed, who works them, removal — is the owner's alone.
// Taking and leaving a chair are the one exception: an operator does that
// for themselves, within the rules TakeSeat and LeaveSeat enforce.

type seatsResponse struct {
	Seats []queue.Seat `json:"seats"`
}

type seatResponse struct {
	Seat queue.Seat `json:"seat"`
}

// seatID reads and shape-checks the {seatId} path value.
func seatID(r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("seatId"))
	if id == "" || !storage.IsUUID(id) {
		return "", false
	}
	return id, true
}

func (s *Server) listSeats(w http.ResponseWriter, r *http.Request) {
	q, _, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}
	seats, err := s.store.Seats(r.Context(), q.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, seatsResponse{Seats: seats})
}

type createSeatRequest struct {
	Name string `json:"name"`
}

func (s *Server) createSeat(w http.ResponseWriter, r *http.Request) {
	q, _, ok := s.requireOwner(w, r)
	if !ok {
		return
	}

	var req createSeatRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}
	name, err := storage.ValidSeatName(req.Name)
	if err != nil {
		writeError(w, err)
		return
	}

	seat, err := s.store.CreateSeat(r.Context(), q.ID, name)
	if err != nil {
		writeError(w, err)
		return
	}

	// A new seat changes the estimate on every pass, so the whole queue
	// hears about it.
	s.publish(r.Context(), q.ID, EventQueueUpdated)
	httpx.JSON(w, http.StatusCreated, seatResponse{Seat: seat})
}

// seatWorkerRequest names who works a chair. Absent leaves it alone; null
// makes the chair nobody's.
type seatWorkerRequest struct {
	Type       string `json:"type"`
	OperatorID string `json:"operatorId"`
}

type updateSeatRequest struct {
	Name     *string            `json:"name"`
	Active   *bool              `json:"active"`
	Position *int               `json:"position"`
	Worker   *seatWorkerRequest `json:"worker"`
}

// updateSeat renames, reorders, opens or closes a chair, and says who works
// it. The worker is decoded through a map so that "worker": null — take
// whoever is there off it — is told apart from a request that did not
// mention the worker at all.
func (s *Server) updateSeat(w http.ResponseWriter, r *http.Request) {
	q, _, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	id, ok := seatID(r)
	if !ok {
		writeError(w, queue.ErrSeatNotFound)
		return
	}

	var raw map[string]json.RawMessage
	if err := httpx.DecodeJSON(w, r, &raw); err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}
	_, workerPresent := raw["worker"]

	req, err := decodeFields[updateSeatRequest](raw)
	if err != nil {
		writeError(w, invalid("We couldn't read that request."))
		return
	}

	params := storage.UpdateSeatParams{Active: req.Active}
	if req.Name != nil {
		name, err := storage.ValidSeatName(*req.Name)
		if err != nil {
			writeError(w, err)
			return
		}
		params.Name = &name
	}
	if req.Position != nil {
		if *req.Position < 1 || *req.Position > 100 {
			writeError(w, invalid("That position doesn't look right."))
			return
		}
		params.Position = req.Position
	}

	seats, err := s.store.UpdateSeat(r.Context(), q.ID, id, params)
	if err != nil {
		writeError(w, err)
		return
	}

	if workerPresent {
		var worker *queue.SeatWorker
		if req.Worker != nil {
			worker = &queue.SeatWorker{Type: queue.PrincipalType(strings.ToUpper(strings.TrimSpace(req.Worker.Type)))}
			switch worker.Type {
			case queue.PrincipalOwner:
			case queue.PrincipalOperator:
				worker.OperatorID = strings.TrimSpace(req.Worker.OperatorID)
				if !storage.IsUUID(worker.OperatorID) {
					writeError(w, queue.ErrOperatorNotFound)
					return
				}
			default:
				writeError(w, invalid("Say who works this chair."))
				return
			}
		}
		seats, err = s.store.AssignSeat(r.Context(), q.ID, id, worker)
		if err != nil {
			writeError(w, err)
			return
		}
	}

	s.publish(r.Context(), q.ID, EventQueueUpdated)
	httpx.JSON(w, http.StatusOK, seatsResponse{Seats: seats})
}

func (s *Server) removeSeat(w http.ResponseWriter, r *http.Request) {
	q, _, ok := s.requireOwner(w, r)
	if !ok {
		return
	}
	id, ok := seatID(r)
	if !ok {
		writeError(w, queue.ErrSeatNotFound)
		return
	}

	seats, err := s.store.RemoveSeat(r.Context(), q.ID, id)
	if err != nil {
		writeError(w, err)
		return
	}

	s.publish(r.Context(), q.ID, EventQueueUpdated)
	httpx.JSON(w, http.StatusOK, seatsResponse{Seats: seats})
}

// takeSeat is the caller sitting down at a chair; leaveSeat is them getting
// up. Both are shared with operators, and the rules — a free chair only,
// never on a queue with fixed chairs — are the store's.
func (s *Server) takeSeat(w http.ResponseWriter, r *http.Request) {
	s.seatForSelf(w, r, s.store.TakeSeat)
}

func (s *Server) leaveSeat(w http.ResponseWriter, r *http.Request) {
	s.seatForSelf(w, r, s.store.LeaveSeat)
}

func (s *Server) seatForSelf(
	w http.ResponseWriter,
	r *http.Request,
	act func(ctx context.Context, queueID, seatID string, actor queue.Actor) ([]queue.Seat, error),
) {
	q, actor, ok := s.requireQueueAccess(w, r)
	if !ok {
		return
	}
	id, ok := seatID(r)
	if !ok {
		writeError(w, queue.ErrSeatNotFound)
		return
	}

	seats, err := act(r.Context(), q.ID, id, actor)
	if err != nil {
		writeError(w, err)
		return
	}

	// Who works a chair is on every counter's tiles, so the dashboards
	// hear about it; the public state does not change, but every frame is
	// a full snapshot anyway.
	s.publish(r.Context(), q.ID, EventQueueUpdated)
	httpx.JSON(w, http.StatusOK, seatsResponse{Seats: seats})
}
