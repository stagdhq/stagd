package relay

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// ErrNotConnected is a relay that isn't connected right now.
var ErrNotConnected = errors.New("the relay isn't connected")

// Hub is Stagd's side: the relays connected to this server, by key (what
// Authorize made of the relay's token). Relays live in this process's
// memory, so with several Stagd servers a relay is only reachable through
// the one it's connected to.
type Hub struct {
	// Authorize turns a relay's token into its key, or refuses it.
	Authorize func(ctx context.Context, token string) (string, error)

	Log *slog.Logger

	mu         sync.Mutex
	links      map[string]*link
	transports map[string]*http.Transport
}

type link struct {
	session *yamux.Session
	info    Info
}

// Info is what a connected relay told Stagd about itself.
type Info struct {
	Version     string
	Kind        string
	ConnectedAt time.Time
}

// ServeHTTP takes a relay's connection and holds it until it drops.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := bearer(r)
	if !ok {
		http.Error(w, "a relay token is needed", http.StatusUnauthorized)

		return
	}

	key, err := h.Authorize(r.Context(), token)
	if err != nil {
		h.Log.Info("relay: refused", "remote", r.RemoteAddr, "err", err)
		http.Error(w, "the relay token isn't valid: it may belong to a deleted compute connection", http.StatusUnauthorized)

		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}

	ws.SetReadLimit(readLimit)

	conn := websocket.NetConn(r.Context(), ws, websocket.MessageBinary)

	session, err := yamux.Client(conn, yamuxConfig())
	if err != nil {
		conn.Close()

		return
	}

	info := Info{Version: r.Header.Get(HeaderVersion), Kind: r.Header.Get(HeaderKind), ConnectedAt: time.Now()}
	h.attach(key, &link{session: session, info: info})

	h.Log.Info("relay: connected", "kind", info.Kind, "version", info.Version, "remote", r.RemoteAddr)

	<-session.CloseChan()

	h.detach(key, session)
	h.Log.Info("relay: disconnected", "kind", info.Kind, "remote", r.RemoteAddr)
}

func bearer(r *http.Request) (string, bool) {
	const prefix = "Bearer "

	v := r.Header.Get("Authorization")
	if len(v) <= len(prefix) || v[:len(prefix)] != prefix {
		return "", false
	}

	return v[len(prefix):], true
}

// attach makes a link the key's relay; one that was there before is closed.
func (h *Hub) attach(key string, l *link) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.links == nil {
		h.links = map[string]*link{}
	}

	if old := h.links[key]; old != nil {
		old.session.Close()
	}

	h.links[key] = l
}

func (h *Hub) detach(key string, session *yamux.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if l := h.links[key]; l != nil && l.session == session {
		delete(h.links, key)
	}

	if t := h.transports[key]; t != nil {
		t.CloseIdleConnections()
	}

	if t := h.transports[key+" api"]; t != nil {
		t.CloseIdleConnections()
	}
}

// Drop disconnects a relay, e.g. when its compute connection is deleted.
func (h *Hub) Drop(key string) {
	h.mu.Lock()
	l := h.links[key]
	h.mu.Unlock()

	if l != nil {
		l.session.Close()
	}
}

// Connected reports on the key's relay.
func (h *Hub) Connected(key string) (Info, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	l := h.links[key]
	if l == nil {
		return Info{}, false
	}

	return l.info, true
}

func (h *Hub) session(key string) (*yamux.Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	l := h.links[key]
	if l == nil || l.session.IsClosed() {
		return nil, ErrNotConnected
	}

	return l.session, nil
}

// Dial connects to an address next to the relay, if the relay allows it.
func (h *Hub) Dial(ctx context.Context, key, addr string) (net.Conn, error) {
	session, err := h.session(key)
	if err != nil {
		return nil, err
	}

	return open(session, header{Op: opDial, Addr: addr})
}

// DialAPI connects to the compute's API, through the relay.
func (h *Hub) DialAPI(ctx context.Context, key string) (net.Conn, error) {
	session, err := h.session(key)
	if err != nil {
		return nil, err
	}

	return open(session, header{Op: opAPI})
}

// Transport sends HTTP requests to addresses next to the relay: the host in
// the request's URL is dialled from there.
func (h *Hub) Transport(key string) http.RoundTripper {
	return h.transport(key, func(ctx context.Context, _, addr string) (net.Conn, error) {
		return h.Dial(ctx, key, addr)
	})
}

// APITransport sends HTTP requests to the compute's API, whatever the URL's host.
func (h *Hub) APITransport(key string) http.RoundTripper {
	return h.transport(key+" api", func(ctx context.Context, _, _ string) (net.Conn, error) {
		return h.DialAPI(ctx, key)
	})
}

// transport is one per key and kind, so connections through the relay are reused.
func (h *Hub) transport(name string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) http.RoundTripper {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.transports == nil {
		h.transports = map[string]*http.Transport{}
	}

	t := h.transports[name]
	if t == nil {
		t = &http.Transport{
			DialContext:         dial,
			MaxIdleConnsPerHost: 8,
			IdleConnTimeout:     60 * time.Second,
		}

		h.transports[name] = t
	}

	return t
}
