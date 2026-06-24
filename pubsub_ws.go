package client

import (
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PubSubWS is a real-time WebSocket client for Localitas DurablePubSub.
// It provides automatic reconnection with exponential backoff, cursor-based
// message delivery (no missed messages), and thread-safe operation.
//
// On disconnect the client re-subscribes to all channels, resuming from
// each consumer's last cursor position on the server side.
//
// Usage:
//
//	ps := client.NewPubSubWS("ws://localhost:8080/apps/cache/ws/my-cache", token)
//	ps.Subscribe("notifications", "worker-1", func(msg client.PubSubMessage) {
//	    fmt.Println("got:", msg.Value)
//	})
//	ps.Publish("events", `{"type":"click"}`)
//	ps.On("connected", func(_ interface{}) { fmt.Println("connected") })
//	defer ps.Close()
type PubSubWS struct {
	url   string
	token string

	reconnectInterval    time.Duration
	maxReconnectInterval time.Duration
	batchSize            int

	mu                sync.Mutex
	conn              *websocket.Conn
	subscriptions     map[string]subscription
	listeners         map[string][]func(interface{})
	reconnectAttempts int
	intentionalClose  bool
	done              chan struct{}
}

type subscription struct {
	consumer string
	callback func(PubSubMessage)
}

// wsOutgoing is the JSON envelope sent to the server.
type wsOutgoing struct {
	Action        string `json:"action"`
	Channel       string `json:"channel,omitempty"`
	Consumer      string `json:"consumer,omitempty"`
	Count         int    `json:"count,omitempty"`
	Value         string `json:"value,omitempty"`
	Group         string `json:"group,omitempty"`
	Seq           int64  `json:"seq,omitempty"`
	MaxSize       int    `json:"max_size,omitempty"`
	MaxAgeSeconds int    `json:"max_age_seconds,omitempty"`
}

// wsIncoming is the JSON envelope received from the server.
type wsIncoming struct {
	Type    string `json:"type"`
	Channel string `json:"channel,omitempty"`
	Seq     int64  `json:"seq,omitempty"`
	Value   string `json:"value,omitempty"`
}

// PubSubWSOption configures a PubSubWS client.
type PubSubWSOption func(*PubSubWS)

// WithReconnectInterval sets the base reconnect delay (default 2s).
func WithReconnectInterval(d time.Duration) PubSubWSOption {
	return func(ps *PubSubWS) { ps.reconnectInterval = d }
}

// WithMaxReconnectInterval sets the reconnect backoff cap (default 30s).
func WithMaxReconnectInterval(d time.Duration) PubSubWSOption {
	return func(ps *PubSubWS) { ps.maxReconnectInterval = d }
}

// WithBatchSize sets the default message batch size for subscribe reads (default 50).
func WithBatchSize(n int) PubSubWSOption {
	return func(ps *PubSubWS) { ps.batchSize = n }
}

// PubSubPublishWSOption configures a single Publish call.
type PubSubPublishWSOption func(*wsOutgoing)

// WithMaxSize bounds the channel by message count on publish.
func WithMaxSize(n int) PubSubPublishWSOption {
	return func(msg *wsOutgoing) { msg.MaxSize = n }
}

// WithMaxAgeSeconds auto-expires messages older than n seconds on publish.
func WithMaxAgeSeconds(n int) PubSubPublishWSOption {
	return func(msg *wsOutgoing) { msg.MaxAgeSeconds = n }
}

// NewPubSubWS creates a WebSocket pub/sub client and connects immediately.
// The url should be a WebSocket URL like ws://host:port/apps/cache/ws/{cache}.
// The token is sent as a query parameter for authentication.
func NewPubSubWS(wsURL, token string, opts ...PubSubWSOption) *PubSubWS {
	ps := &PubSubWS{
		url:                  wsURL,
		token:                token,
		reconnectInterval:    2 * time.Second,
		maxReconnectInterval: 30 * time.Second,
		batchSize:            50,
		subscriptions:        make(map[string]subscription),
		listeners:            make(map[string][]func(interface{})),
		done:                 make(chan struct{}),
	}
	for _, o := range opts {
		o(ps)
	}
	ps.connect()
	return ps
}

// Subscribe registers a callback for messages on a channel. The consumer ID
// is used for server-side cursor tracking so no messages are missed across
// reconnects. Only one subscription per channel is active at a time.
func (ps *PubSubWS) Subscribe(channel, consumer string, callback func(PubSubMessage)) {
	ps.mu.Lock()
	ps.subscriptions[channel] = subscription{consumer: consumer, callback: callback}
	conn := ps.conn
	ps.mu.Unlock()

	if conn != nil {
		ps.send(conn, wsOutgoing{
			Action:   "subscribe",
			Channel:  channel,
			Consumer: consumer,
			Count:    ps.batchSize,
		})
	}
}

// Unsubscribe removes the subscription for a channel.
func (ps *PubSubWS) Unsubscribe(channel string) {
	ps.mu.Lock()
	delete(ps.subscriptions, channel)
	conn := ps.conn
	ps.mu.Unlock()

	if conn != nil {
		ps.send(conn, wsOutgoing{Action: "unsubscribe", Channel: channel})
	}
}

// Publish sends a message to a channel. Optional PubSubPublishWSOption values
// can set max_size or max_age_seconds retention policies.
func (ps *PubSubWS) Publish(channel, value string, opts ...PubSubPublishWSOption) {
	msg := wsOutgoing{Action: "publish", Channel: channel, Value: value}
	for _, o := range opts {
		o(&msg)
	}

	ps.mu.Lock()
	conn := ps.conn
	ps.mu.Unlock()

	if conn != nil {
		ps.send(conn, msg)
	}
}

// Ack acknowledges a consumer group message by sequence number.
func (ps *PubSubWS) Ack(channel, group string, seq int64) {
	ps.mu.Lock()
	conn := ps.conn
	ps.mu.Unlock()

	if conn != nil {
		ps.send(conn, wsOutgoing{Action: "ack", Channel: channel, Group: group, Seq: seq})
	}
}

// On registers an event listener. Supported events: "connected",
// "disconnected", "error", "reconnecting", "message", "published",
// "subscribed", "unsubscribed", "acked".
func (ps *PubSubWS) On(event string, callback func(interface{})) {
	ps.mu.Lock()
	ps.listeners[event] = append(ps.listeners[event], callback)
	ps.mu.Unlock()
}

// Close disconnects from the server without auto-reconnecting.
func (ps *PubSubWS) Close() {
	ps.mu.Lock()
	ps.intentionalClose = true
	conn := ps.conn
	ps.conn = nil
	ps.mu.Unlock()

	if conn != nil {
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		conn.Close()
	}
}

// connect dials the WebSocket server in a background goroutine.
func (ps *PubSubWS) connect() {
	u := ps.url
	if ps.token != "" {
		parsed, err := url.Parse(u)
		if err == nil {
			q := parsed.Query()
			q.Set("token", ps.token)
			parsed.RawQuery = q.Encode()
			u = parsed.String()
		}
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	header := http.Header{}
	if ps.token != "" {
		header.Set("Authorization", "Bearer "+ps.token)
	}

	conn, _, err := dialer.Dial(u, header)
	if err != nil {
		ps.emit("error", err)
		ps.scheduleReconnect()
		return
	}

	ps.mu.Lock()
	ps.conn = conn
	ps.reconnectAttempts = 0

	subs := make(map[string]subscription, len(ps.subscriptions))
	for ch, sub := range ps.subscriptions {
		subs[ch] = sub
	}
	ps.mu.Unlock()

	ps.emit("connected", nil)

	for ch, sub := range subs {
		ps.send(conn, wsOutgoing{
			Action:   "subscribe",
			Channel:  ch,
			Consumer: sub.consumer,
			Count:    ps.batchSize,
		})
	}

	go ps.readLoop(conn)
}

// readLoop reads messages from the WebSocket until the connection closes.
func (ps *PubSubWS) readLoop(conn *websocket.Conn) {
	defer func() {
		conn.Close()

		ps.mu.Lock()
		if ps.conn == conn {
			ps.conn = nil
		}
		intentional := ps.intentionalClose
		ps.mu.Unlock()

		ps.emit("disconnected", nil)

		if !intentional {
			ps.scheduleReconnect()
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var msg wsIncoming
		if json.Unmarshal(data, &msg) != nil {
			continue
		}

		ps.emit(msg.Type, msg)

		if msg.Type == "message" && msg.Channel != "" {
			ps.mu.Lock()
			sub, ok := ps.subscriptions[msg.Channel]
			ps.mu.Unlock()

			if ok && sub.callback != nil {
				sub.callback(PubSubMessage{
					Seq:   msg.Seq,
					Value: msg.Value,
				})
			}
		}
	}
}

// scheduleReconnect waits with exponential backoff then reconnects.
func (ps *PubSubWS) scheduleReconnect() {
	ps.mu.Lock()
	if ps.intentionalClose {
		ps.mu.Unlock()
		return
	}
	ps.reconnectAttempts++
	attempt := ps.reconnectAttempts

	delay := time.Duration(float64(ps.reconnectInterval) * math.Pow(1.5, float64(attempt-1)))
	if delay >= ps.maxReconnectInterval {
		delay = ps.maxReconnectInterval
		ps.reconnectAttempts = 0
	}
	ps.mu.Unlock()

	ps.emit("reconnecting", map[string]interface{}{
		"attempt": attempt,
		"delay":   delay,
	})

	time.AfterFunc(delay, func() {
		ps.mu.Lock()
		intentional := ps.intentionalClose
		ps.mu.Unlock()
		if !intentional {
			ps.connect()
		}
	})
}

// send marshals and writes a message to the WebSocket connection.
func (ps *PubSubWS) send(conn *websocket.Conn, msg wsOutgoing) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	conn.WriteMessage(websocket.TextMessage, data)
}

// emit dispatches an event to registered listeners.
func (ps *PubSubWS) emit(event string, data interface{}) {
	ps.mu.Lock()
	cbs := make([]func(interface{}), len(ps.listeners[event]))
	copy(cbs, ps.listeners[event])
	ps.mu.Unlock()

	for _, cb := range cbs {
		cb(data)
	}
}
