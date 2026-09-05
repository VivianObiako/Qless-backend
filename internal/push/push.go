// Package push sends the three nudges — getting close, next, your turn — to a
// phone that has put the pass away. It is the difference between "walk away"
// being true and being a tagline: the in-page notification needs the tab
// alive, and Android Chrome refuses it outright.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// ErrGone is the push service saying this subscription no longer exists: the
// customer cleared site data, or the browser rotated it. Delete and move on.
var ErrGone = errors.New("push subscription gone")

// Subscription is what a browser hands back from PushManager.subscribe.
type Subscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// Message is what the service worker shows. URL is where a tap lands.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
}

// Sender holds the VAPID pair. A Sender with no keys is a valid, disabled one,
// so the server runs without push configured and simply never offers it.
type Sender struct {
	public  string
	private string
	subject string
	client  *http.Client
}

func New(public, private, subject string) *Sender {
	return &Sender{public: public, private: private, subject: subject, client: &http.Client{}}
}

func (s *Sender) Enabled() bool {
	return s != nil && s.public != "" && s.private != ""
}

func (s *Sender) PublicKey() string {
	if s == nil {
		return ""
	}
	return s.public
}

// Send delivers one message. A 404 or 410 from the push service is ErrGone;
// anything else that fails is an error worth logging and not retrying here.
func (s *Sender) Send(ctx context.Context, sub Subscription, msg Message) error {
	if !s.Enabled() {
		return errors.New("push is not configured")
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	res, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      s.subject,
		VAPIDPublicKey:  s.public,
		VAPIDPrivateKey: s.private,
		// A nudge about a queue is worthless an hour later.
		TTL:     600,
		Urgency: webpush.UrgencyHigh,
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)

	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return ErrGone
	case res.StatusCode >= 400:
		return fmt.Errorf("push service answered %d", res.StatusCode)
	}
	return nil
}

// GenerateKeys mints a VAPID pair for a deployment. Run once, keep the
// private key secret, and never rotate casually: every subscription is bound
// to the public key it was made with.
func GenerateKeys() (private, public string, err error) {
	return webpush.GenerateVAPIDKeys()
}
