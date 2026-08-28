package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// socketEvent is the envelope both audiences share. State and View are raw so a
// test can assert on exactly what crossed the wire, not on a struct that might
// silently drop a field.
type socketEvent struct {
	Type  string          `json:"type"`
	State json.RawMessage `json:"state"`
	View  json.RawMessage `json:"view"`
	raw   []byte
}

// dial opens a socket and consumes the snapshot the server sends on connect.
// Returning only after that frame has arrived is what makes the tests
// deterministic: the subscription provably exists before anything is published.
func (c *testClient) dial(path string) (*websocket.Conn, socketEvent) {
	c.t.Helper()

	url := "ws" + strings.TrimPrefix(c.server.URL, "http") + path

	conn, res, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		c.t.Fatalf("dial %s: %v (status %d)", path, err, status)
	}
	c.t.Cleanup(func() { _ = conn.Close() })

	snapshot := readEvent(c.t, conn)
	if snapshot.Type != "QUEUE_UPDATED" {
		c.t.Fatalf("first frame = %q, want QUEUE_UPDATED", snapshot.Type)
	}
	return conn, snapshot
}

func readEvent(t *testing.T, conn *websocket.Conn) socketEvent {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}

	_, frame, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}

	var event socketEvent
	if err := json.Unmarshal(frame, &event); err != nil {
		t.Fatalf("decode frame %s: %v", frame, err)
	}
	event.raw = frame
	return event
}

// A customer's phone and the operator's dashboard both learn about a join
// without asking. This is the whole point of milestone 2.
func TestSocketBroadcastsJoin(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Realtime Barbershop")

	customer, _ := client.dial("/api/queues/" + created.Queue.Slug + "/ws")
	operator, _ := client.dial("/api/queues/" + created.Queue.ID + "/ws?k=" + created.OwnerToken)

	if _, res := client.join(created.Queue.Slug, "Ada", ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	event := readEvent(t, customer)
	if event.Type != "CUSTOMER_JOINED" {
		t.Errorf("customer frame type = %q, want CUSTOMER_JOINED", event.Type)
	}

	var state struct {
		WaitingNumbers []int `json:"waitingNumbers"`
		WaitingCount   int   `json:"waitingCount"`
	}
	if err := json.Unmarshal(event.State, &state); err != nil {
		t.Fatalf("decode public state: %v", err)
	}
	if len(state.WaitingNumbers) != 1 || state.WaitingNumbers[0] != 1 {
		t.Errorf("waitingNumbers = %v, want [1]", state.WaitingNumbers)
	}
	if state.WaitingCount != 1 {
		t.Errorf("waitingCount = %d, want 1", state.WaitingCount)
	}

	operatorEvent := readEvent(t, operator)
	if operatorEvent.Type != "CUSTOMER_JOINED" {
		t.Errorf("operator frame type = %q, want CUSTOMER_JOINED", operatorEvent.Type)
	}
	if !strings.Contains(string(operatorEvent.View), "Ada") {
		t.Errorf("operator frame carries no customer name: %s", operatorEvent.View)
	}
}

// Serving next must reach the customer being called. Nothing else in the
// product matters if this frame does not arrive.
func TestSocketBroadcastsServeNext(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Realtime Serve Shop")

	if _, res := client.join(created.Queue.Slug, "Ada", ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	customer, snapshot := client.dial("/api/queues/" + created.Queue.Slug + "/ws")

	var initial struct {
		ServingNumber *int `json:"servingNumber"`
	}
	if err := json.Unmarshal(snapshot.State, &initial); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if initial.ServingNumber != nil {
		t.Fatalf("servingNumber = %v on connect, want nil", *initial.ServingNumber)
	}

	res := client.do(http.MethodPost, "/api/queues/"+created.Queue.ID+"/next", nil,
		header{"Authorization", "Bearer " + created.OwnerToken})
	if res.status != http.StatusOK {
		t.Fatalf("serve next: status %d, body %s", res.status, res.body)
	}

	event := readEvent(t, customer)
	if event.Type != "CUSTOMER_SERVED" {
		t.Errorf("frame type = %q, want CUSTOMER_SERVED", event.Type)
	}

	var state struct {
		ServingNumber  *int  `json:"servingNumber"`
		WaitingNumbers []int `json:"waitingNumbers"`
	}
	if err := json.Unmarshal(event.State, &state); err != nil {
		t.Fatalf("decode public state: %v", err)
	}
	if state.ServingNumber == nil || *state.ServingNumber != 1 {
		t.Fatalf("servingNumber = %v, want 1", state.ServingNumber)
	}
	if len(state.WaitingNumbers) != 0 {
		t.Errorf("waitingNumbers = %v, want empty", state.WaitingNumbers)
	}
}

// The public feed reaches every phone in the shop and the lobby display. A name
// on this socket would hand one customer's identity to everyone waiting.
func TestPublicSocketFramesContainNoCustomerNames(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Socket Privacy Shop")

	const secretName = "Zebediah Q Featherstonehaugh"

	customer, snapshot := client.dial("/api/queues/" + created.Queue.Slug + "/ws")

	if _, res := client.join(created.Queue.Slug, secretName, ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}
	joined := readEvent(t, customer)

	res := client.do(http.MethodPost, "/api/queues/"+created.Queue.ID+"/next", nil,
		header{"Authorization", "Bearer " + created.OwnerToken})
	if res.status != http.StatusOK {
		t.Fatalf("serve next: status %d, body %s", res.status, res.body)
	}
	served := readEvent(t, customer)

	for _, event := range []socketEvent{snapshot, joined, served} {
		if strings.Contains(string(event.raw), secretName) {
			t.Errorf("public %s frame leaked a customer name: %s", event.Type, event.raw)
		}
		if event.View != nil {
			t.Errorf("public %s frame carried an operator view: %s", event.Type, event.View)
		}
	}
}

// The socket is an operator endpoint when it carries names, so it is subject to
// exactly the same check as every other one.
func TestOperatorSocketRejectsBadOwnerToken(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Socket Authorization Shop")

	tokens := map[string]string{
		"wrong token":       "not-the-owner-token",
		"another queue":     client.createQueue("Other Shop").OwnerToken,
		"truncated token":   created.OwnerToken[:len(created.OwnerToken)-1],
		"empty-ish padding": strings.Repeat("a", 43),
	}

	url := "ws" + strings.TrimPrefix(client.server.URL, "http")

	for name, badToken := range tokens {
		t.Run(name, func(t *testing.T) {
			conn, res, err := websocket.DefaultDialer.Dial(
				url+"/api/queues/"+created.Queue.ID+"/ws?k="+badToken, nil)
			if err == nil {
				_ = conn.Close()
				t.Fatal("handshake succeeded, want rejection")
			}
			if res == nil || res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %v, want 401", res)
			}
		})
	}
}

// A socket opened without a token is a customer's socket, and stays one for as
// long as it is connected. There is no message it can send to become an
// operator, because it is never read for anything but pongs.
func TestSocketWithoutTokenNeverReceivesNames(t *testing.T) {
	client := newTestClient(t)
	created := client.createQueue("Socket Downgrade Shop")

	anonymous, _ := client.dial("/api/queues/" + created.Queue.ID + "/ws")

	if _, res := client.join(created.Queue.Slug, "Ada", ""); res.status != http.StatusCreated {
		t.Fatalf("join: status %d, body %s", res.status, res.body)
	}

	if err := anonymous.WriteMessage(websocket.TextMessage,
		[]byte(`{"audience":"operator","k":"`+created.OwnerToken+`"}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	event := readEvent(t, anonymous)
	if event.View != nil || strings.Contains(string(event.raw), "Ada") {
		t.Errorf("frame after an upgrade attempt carried operator data: %s", event.raw)
	}
}
