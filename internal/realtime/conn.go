package realtime

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// A queue event is small, so a client that is more than a handful of
	// events behind is not slow — it is gone. Disconnecting it is kinder than
	// buffering for a browser that will refetch on reconnect anyway.
	sendBuffer = 16

	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512
)

// Client is one browser watching one queue.
type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	queueID  string
	audience Audience

	mu     sync.Mutex
	send   chan []byte
	closed bool
}

// deliver queues a frame, or disconnects the client if its buffer is full.
func (c *Client) deliver(frame []byte) {
	if frame == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	select {
	case c.send <- frame:
	default:
		c.closed = true
		close(c.send)
	}
}

// stop ends the write pump. Closing the send channel is the only shutdown
// signal, so it happens exactly once and always under the lock that deliver
// holds — a send on a closed channel would take the whole server down.
func (c *Client) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
}

// Upgrader turns an HTTP request into a hub subscription.
type Upgrader struct {
	hub      *Hub
	upgrader websocket.Upgrader
}

// NewUpgrader restricts upgrades to the configured web origin. A WebSocket
// handshake is not subject to CORS, so this check is the only thing standing
// between another site and a live feed of this queue.
func NewUpgrader(hub *Hub, allowedOrigin string) *Upgrader {
	return &Upgrader{
		hub: hub,
		upgrader: websocket.Upgrader{
			HandshakeTimeout: 10 * time.Second,
			ReadBufferSize:   1024,
			WriteBufferSize:  4096,
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				// No Origin means a non-browser client, which cannot be a
				// third-party site acting with someone's cookies.
				return origin == "" || allowedOrigin == "*" || origin == allowedOrigin
			},
		},
	}
}

// Serve upgrades the connection, subscribes it, and blocks until the client
// disconnects.
//
// snapshot is called after the subscription exists, not before: an event
// published while the snapshot is being built is then already queued ahead of
// it, and since the snapshot reads state committed after that event, applying
// them in order still leaves the browser correct. Building it first would open
// a window where that event is missed entirely.
func (u *Upgrader) Serve(
	w http.ResponseWriter,
	r *http.Request,
	queueID string,
	audience Audience,
	snapshot func() []byte,
) {
	conn, err := u.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written its own error response.
		slog.Debug("websocket upgrade failed", "error", err)
		return
	}

	client := &Client{
		hub:      u.hub,
		conn:     conn,
		queueID:  queueID,
		audience: audience,
		send:     make(chan []byte, sendBuffer),
	}

	u.hub.add(client)
	client.deliver(snapshot())

	go client.writePump()
	client.readPump()
}

// readPump keeps the connection honest. Customers never send us anything, so
// every read is either a pong, a close, or a client we want to drop.
func (c *Client) readPump() {
	defer func() {
		c.hub.remove(c)
		c.stop()
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case frame, open := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !open {
				_ = c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}

		case <-ticker.C:
			// Pings are what detect a phone that went into a tunnel: without
			// them the connection would look healthy until the customer tried
			// to act on a position that stopped updating half an hour ago.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
