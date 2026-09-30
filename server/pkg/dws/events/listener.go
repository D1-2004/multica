package events

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Alert is an outage or its end, for the host to surface (product page,
// DingTalk message, pager).
type Alert struct {
	Identity string
	// Kind is "down" (not healthy for longer than AlertAfter), "recovered",
	// or "retired" (the identity left the configuration while alerting: the
	// alert is closed without a recovery).
	Kind  string
	Since time.Time // when the outage began
	Err   string
}

// Options tune a Listener (and every Listener a Manager runs).
type Options struct {
	// ReadIdle reconnects when no frame arrived for this long (default 3m;
	// the server pings well within that).
	ReadIdle time.Duration
	// MinBackoff and MaxBackoff bound the reconnect delay (1s, 30s).
	MinBackoff, MaxBackoff time.Duration
	// AlertAfter is how long an outage lasts before Alert fires (2m).
	AlertAfter time.Duration
	// StableAfter is how long a connection must stay up before it ends an
	// outage (30s), so a connection that drops right after the handshake
	// keeps the outage running. A handled event ends it at once. After the
	// host failed to handle an event, only a handled event ends it.
	StableAfter time.Duration
	// DedupeTTL is how long an event id is remembered (24h).
	DedupeTTL time.Duration
	// Alert is called on outages and recoveries.
	Alert func(ctx context.Context, a Alert)
	// Holder names this replica in Status and leases (default host:pid).
	Holder string
	Dialer *websocket.Dialer
	Now    func() time.Time
}

func (o Options) readIdle() time.Duration    { return orDefault(o.ReadIdle, 3*time.Minute) }
func (o Options) minBackoff() time.Duration  { return orDefault(o.MinBackoff, time.Second) }
func (o Options) maxBackoff() time.Duration  { return orDefault(o.MaxBackoff, 30*time.Second) }
func (o Options) alertAfter() time.Duration  { return orDefault(o.AlertAfter, 2*time.Minute) }
func (o Options) stableAfter() time.Duration { return orDefault(o.StableAfter, 30*time.Second) }
func (o Options) dedupeTTL() time.Duration   { return orDefault(o.DedupeTTL, 24*time.Hour) }
func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func orDefault(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

// Listener keeps one identity's personal event stream connected.
type Listener struct {
	Identity string
	// Client returns the identity's Client; a dws.Pool keeps it alive.
	Client func(ctx context.Context) (*dws.Client, error)
	// Subscriptions is the configured set; Run reconciles DingTalk's side
	// against it before every connection.
	Subscriptions []dws.SubscriptionSpec
	// Handle must persist the event before returning nil: the event is
	// acked, and DingTalk will not redeliver it, only after that. An error
	// leaves it unacked and reconnects.
	Handle func(ctx context.Context, ev Event) error
	Store  Store
	Options
}

var errServerDisconnect = errors.New("server asked to reconnect")

// errHandleFailed marks a connection that ended because the host could not
// handle an event: the stream works, the pipeline does not.
var errHandleFailed = errors.New("host failed to handle an event")

// connectTimeout bounds the setup of one connection (client, subscription
// reconciliation, ticket, dial), so an outage always surfaces as an error
// the alerting can see.
const connectTimeout = 90 * time.Second

// Run keeps the stream connected until ctx is done.
func (l *Listener) Run(ctx context.Context) error {
	if l.Identity == "" || l.Client == nil || l.Handle == nil || l.Store == nil {
		return errors.New("events: listener needs Identity, Client, Handle and Store")
	}
	now := l.now()
	st := Status{Identity: l.Identity, Holder: l.Holder, State: StateConnecting, Since: now, DownSince: now}
	// A restart, a takeover or a configuration change continues the outage
	// (and its alert) the previous run recorded, however that run ended; a
	// run that ended healthy left no outage behind.
	if prev, ok, err := l.Store.Status(ctx, l.Identity); err == nil && ok {
		if !prev.DownSince.IsZero() {
			st.DownSince = prev.DownSince
		}
		st.Alerting, st.HandleFailing = prev.Alerting, prev.HandleFailing
	}
	l.publish(ctx, &st)
	backoff := l.minBackoff()
	for {
		if ctx.Err() != nil {
			st.State, st.Since = StateStopped, l.now()
			l.publish(context.WithoutCancel(ctx), &st)
			return nil
		}
		handled, err := l.connect(ctx, &st)
		if ctx.Err() != nil {
			continue
		}
		if errors.Is(err, errHandleFailed) {
			st.HandleFailing = true // cleared only by a handled event
		}
		now := l.now()
		if st.DownSince.IsZero() {
			st.DownSince = now
		}
		st.State, st.Since, st.LastError = StateReconnecting, now, err.Error()
		st.Failures++
		if handled > 0 || errors.Is(err, errServerDisconnect) {
			backoff = l.minBackoff()
		}
		l.alertIfDown(ctx, &st, err.Error())
		l.publish(ctx, &st)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, l.maxBackoff())
	}
}

// alertIfDown raises the "down" alert once the outage outlasts AlertAfter.
func (l *Listener) alertIfDown(ctx context.Context, st *Status, reason string) {
	if st.Alerting || st.DownSince.IsZero() || l.now().Sub(st.DownSince) < l.alertAfter() {
		return
	}
	st.Alerting = true
	l.alert(ctx, Alert{Identity: l.Identity, Kind: "down", Since: st.DownSince, Err: reason})
}

type readResult struct {
	raw []byte
	err error
}

// connect runs one connection: client, subscriptions, ticket, dial, frames.
// It returns how many events it handled and why it ended. The outage ends
// once the connection proved healthy: it stayed up for StableAfter (unless
// the host is failing events), or the host handled an event. An outage that
// outlasts AlertAfter while connected but not yet healthy alerts too.
func (l *Listener) connect(ctx context.Context, st *Status) (int, error) {
	setup, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	client, err := l.Client(setup)
	if err != nil {
		return 0, fmt.Errorf("client: %w", err)
	}
	if err := l.reconcile(setup, client, st); err != nil {
		return 0, fmt.Errorf("subscriptions: %w", err)
	}
	ticket, err := client.Events.Ticket(setup)
	if err != nil {
		return 0, fmt.Errorf("ticket: %w", err)
	}
	dialer := l.Dialer
	if dialer == nil {
		dialer = &websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 15 * time.Second}
	}
	conn, _, err := dialer.DialContext(setup, ticket.URL(), nil)
	if err != nil {
		// The dial error may quote the URL, ticket included.
		msg := strings.ReplaceAll(err.Error(), ticket.Ticket, "[redacted]")
		msg = strings.ReplaceAll(msg, url.QueryEscape(ticket.Ticket), "[redacted]")
		return 0, errors.New("dial: " + msg)
	}
	defer conn.Close()
	// Closing the socket is the only way to interrupt a blocked read.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	now := l.now()
	st.State, st.Since, st.LastConnectedAt = StateConnected, now, now
	l.publish(ctx, st)
	healthy := false
	markHealthy := func() {
		if healthy {
			return
		}
		healthy = true
		st.Failures, st.LastError, st.HandleFailing = 0, "", false
		if st.Alerting {
			l.alert(ctx, Alert{Identity: l.Identity, Kind: "recovered", Since: st.DownSince})
		}
		st.Alerting, st.DownSince = false, time.Time{}
		l.publish(ctx, st)
	}
	var stable <-chan time.Time
	if !st.HandleFailing {
		timer := time.NewTimer(l.stableAfter())
		defer timer.Stop()
		stable = timer.C
	}
	// The outage may outlast AlertAfter on this connection: pings keep it
	// open while nothing proves it healthy.
	var overdue <-chan time.Time
	if !st.Alerting && !st.DownSince.IsZero() {
		timer := time.NewTimer(max(st.DownSince.Add(l.alertAfter()).Sub(l.now()), 0))
		defer timer.Stop()
		overdue = timer.C
	}

	// One goroutine reads; this one owns the status and every write.
	frames := make(chan readResult, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			_ = conn.SetReadDeadline(time.Now().Add(l.readIdle()))
			_, raw, err := conn.ReadMessage()
			select {
			case frames <- readResult{raw, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	handled := 0
	lastPublish := time.Time{}
	for {
		var r readResult
		select {
		case <-stable:
			markHealthy()
			continue
		case <-overdue:
			if !healthy {
				reason := st.LastError
				if st.HandleFailing {
					reason = "connected, but the host has not handled an event since: " + reason
				}
				l.alertIfDown(ctx, st, reason)
				l.publish(ctx, st)
			}
			continue
		case r = <-frames:
		}
		if r.err != nil {
			return handled, fmt.Errorf("read: %w", r.err)
		}
		var f frame
		var ok bool
		if json.Unmarshal(r.raw, &f) != nil {
			// No header to ack it by: record it once and keep reading, since
			// reconnecting would not make it readable.
			ok, err = l.deliver(ctx, unreadableEvent(r.raw), nil)
		} else {
			switch f.Type {
			case "SYSTEM":
				switch f.header("topic") {
				case "ping":
					err = reply(conn, f, 200, "ok", f.Data)
				case "disconnect":
					_ = reply(conn, f, 200, "", "")
					return handled, errServerDisconnect
				default:
					err = reply(conn, f, 404, "unknown system topic", "")
				}
			case "EVENT", "CALLBACK":
				ok, err = l.deliver(ctx, frameEvent(f), func() error { return reply(conn, f, 200, "", ackSuccess) })
			}
		}
		if ok {
			handled++
			markHealthy()
			st.LastEventAt = l.now()
			if st.LastEventAt.Sub(lastPublish) > 5*time.Second {
				lastPublish = st.LastEventAt
				l.publish(ctx, st)
			}
		}
		if err != nil {
			return handled, err
		}
	}
}

// frameEvent decodes an event frame; one that does not decode is still the
// host's to record, flagged Malformed.
func frameEvent(f frame) Event {
	ev, err := decodeEvent(f)
	if err == nil {
		return ev
	}
	sum := sha256.Sum256([]byte(f.Data))
	voip := f.header(keyHeaders...) == dws.EventVoIPInvite || topic(f) == dws.EventVoIPInvite
	return Event{ID: firstNonEmpty(f.header("eventId", "event_id", "messageId", "MESSAGE_ID"), "malformed:"+hex.EncodeToString(sum[:8])),
		Key: firstNonEmpty(f.header(keyHeaders...), topic(f)), Malformed: true, Data: safeRawData(unwrapString(f.Data), voip)}
}

// unwrapString undoes one level of JSON string encoding, the form frame
// data usually arrives in.
func unwrapString(data string) string {
	var s string
	if json.Unmarshal([]byte(data), &s) == nil {
		return s
	}
	return data
}

// unreadableEvent is a frame that is not JSON at all.
func unreadableEvent(raw []byte) Event {
	sum := sha256.Sum256(raw)
	return Event{ID: "malformed:" + hex.EncodeToString(sum[:8]), Malformed: true, Data: safeRawData(string(raw), false)}
}

// deliver dedupes, hands the event to the host, and acks (when the frame
// can be acked) only after the host succeeded. done reports a newly handled
// event.
func (l *Listener) deliver(ctx context.Context, ev Event, ack func() error) (bool, error) {
	if ack == nil {
		ack = func() error { return nil }
	}
	seen, err := l.Store.Handled(ctx, l.Identity, ev.ID)
	if err != nil {
		return false, fmt.Errorf("dedupe: %w", err)
	}
	if seen {
		return false, ack()
	}
	if err := l.Handle(ctx, ev); err != nil {
		// Unacked and unmarked: the redelivery is handled again.
		return false, fmt.Errorf("%w %s: %w", errHandleFailed, firstNonEmpty(ev.Key, ev.ID), err)
	}
	// Handled: a failed mark only risks a second, idempotent Handle.
	_ = l.Store.MarkHandled(ctx, l.Identity, ev.ID, l.dedupeTTL())
	return true, ack()
}

// rawData keeps undecodable frame data as JSON (a string when it is not JSON).
func rawData(data string) json.RawMessage {
	if json.Valid([]byte(data)) {
		return json.RawMessage(data)
	}
	quoted, _ := json.Marshal(data)
	return quoted
}

const ackSuccess = `{"status":"SUCCESS","message":"success"}`

func reply(conn *websocket.Conn, f frame, code int, message, data string) error {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(map[string]any{
		"code":    code,
		"headers": map[string]string{"messageId": f.header("messageId"), "contentType": "application/json"},
		"message": message,
		"data":    data,
	})
}

// reconcile makes DingTalk's subscriptions match l.Subscriptions. Only the
// subscriptions this SDK created (kept in the Store) are touched.
//
// Ownership is recorded before a subscription is created, with the spec it
// is created from: a crash between creating and recording would otherwise
// leak it. Subscribing a spec again returns the same ID (DWS dedupes on the
// idempotency key), which is how such a pending record is resolved.
func (l *Listener) reconcile(ctx context.Context, client *dws.Client, st *Status) error {
	desired := map[string]dws.SubscriptionSpec{}
	for _, spec := range l.Subscriptions {
		if err := spec.Validate(); err != nil {
			return err
		}
		desired[spec.Fingerprint()] = spec
	}
	stored, err := l.Store.Subscriptions(ctx, l.Identity)
	if err != nil {
		return err
	}
	live, err := client.Events.List(ctx)
	if err != nil {
		return err
	}
	status := map[string]int{}
	for _, s := range live {
		status[s.ID] = s.Status
	}
	active := func(id string) bool {
		st, ok := status[id]
		return ok && (st == 1 || st == 0) // 0: status not reported
	}

	var keep []dws.Subscription
	satisfied := map[string]bool{}
	var errs error
	// record keeps one record per subscription id: DWS returns the same id
	// for the same spec, so a record can come back through Subscribe.
	record := func(sub dws.Subscription) {
		if sub.ID != "" {
			for i := range keep {
				if keep[i].ID == sub.ID {
					keep[i] = sub
					return
				}
			}
		}
		keep = append(keep, sub)
	}
	kept := func(id string) bool {
		for _, sub := range keep {
			if sub.ID == id {
				return true
			}
		}
		return false
	}
	cancel := func(sub dws.Subscription) {
		if err := client.Events.Cancel(ctx, sub.ID); err != nil {
			record(sub) // stays on record until the cancel succeeds
			errs = errors.Join(errs, err)
		}
	}
	for _, sub := range stored {
		_, want := desired[sub.Fingerprint]
		switch {
		case sub.ID == "":
			// Pending from a crash. Wanted: created below (same ID).
			// Unwanted: resolve its ID from the spec, then cancel it.
			if want || sub.Spec == nil {
				continue
			}
			resolved, err := client.Events.Subscribe(ctx, *sub.Spec)
			if err != nil {
				record(sub)
				errs = errors.Join(errs, err)
				continue
			}
			cancel(resolved)
		case kept(sub.ID):
			// A second record of a subscription already kept: cancelling it
			// would cancel the kept one.
		case want && active(sub.ID) && !satisfied[sub.Fingerprint]:
			satisfied[sub.Fingerprint] = true
			record(sub)
		case status[sub.ID] == 3:
			// Deleted on DingTalk's side: forget it (recreated if wanted).
		case statusKnown(status, sub.ID):
			// Unwanted, a duplicate, or paused (delivers nothing): cancel.
			// A wanted one is recreated below; the same ID comes back active.
			cancel(sub)
		}
		// Gone from the list: dropped, and recreated below if wanted.
	}
	var pending []dws.SubscriptionSpec
	for fp, spec := range desired {
		if !satisfied[fp] {
			pending = append(pending, spec)
		}
	}
	if len(pending) > 0 {
		owned := append([]dws.Subscription(nil), keep...)
		for _, spec := range pending {
			spec := spec
			owned = append(owned, dws.Subscription{EventKey: spec.EventKey, Fingerprint: spec.Fingerprint(), Spec: &spec})
		}
		if err := l.Store.SetSubscriptions(ctx, l.Identity, owned); err != nil {
			return err
		}
		for _, spec := range pending {
			sub, err := client.Events.Subscribe(ctx, spec)
			if err != nil {
				// Keep the pending record: the next reconcile retries it.
				spec := spec
				record(dws.Subscription{EventKey: spec.EventKey, Fingerprint: spec.Fingerprint(), Spec: &spec})
				errs = errors.Join(errs, err)
				continue
			}
			satisfied[spec.Fingerprint()] = true
			record(sub)
		}
	}
	if err := l.Store.SetSubscriptions(ctx, l.Identity, keep); err != nil {
		return err
	}
	st.Subscriptions = len(satisfied)
	return errs
}

func statusKnown(status map[string]int, id string) bool {
	_, ok := status[id]
	return ok
}

func (l *Listener) publish(ctx context.Context, st *Status) {
	st.UpdatedAt = l.now()
	_ = l.Store.SetStatus(ctx, *st)
}

func (l *Listener) alert(ctx context.Context, a Alert) {
	if l.Options.Alert != nil {
		l.Options.Alert(ctx, a)
	}
}
