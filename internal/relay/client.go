package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Relay is the relay's side: it dials Stagd and serves the streams Stagd opens.
type Relay struct {
	// URL is Stagd's address, e.g. https://stagd.example.com.
	URL string

	// Token is the compute connection's relay token.
	Token string

	Version string

	// Kind is what the relay fronts: "kubernetes" or "docker".
	Kind string

	// Allow says whether Stagd may dial an address; an error says why not.
	Allow func(addr string) error

	// API serves Stagd's calls to the compute's API; nil refuses them.
	API http.Handler

	Log *slog.Logger
}

// errRefused is Stagd turning the token down: retrying soon won't help.
var errRefused = errors.New("stagd refused the relay token")

// Run keeps the relay connected until ctx ends, reconnecting with backoff.
func (r *Relay) Run(ctx context.Context) error {
	wait := time.Second

	for {
		started := time.Now()

		err := r.serve(ctx)
		if ctx.Err() != nil {
			return nil
		}

		// A connection that held a while starts the backoff over.
		if time.Since(started) > time.Minute {
			wait = time.Second
		}

		if errors.Is(err, errRefused) {
			wait = max(wait, time.Minute)
		}

		r.Log.Warn("relay: disconnected", "err", err, "retry_in", wait)

		select {
		case <-ctx.Done():
			return nil

		case <-time.After(wait):
		}

		wait = min(wait*2, 5*time.Minute)
	}
}

// serve is one connection to Stagd, until it drops.
func (r *Relay) serve(ctx context.Context) error {
	target := strings.TrimRight(r.URL, "/") + Path

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+r.Token)
	headers.Set(HeaderVersion, r.Version)
	headers.Set(HeaderKind, r.Kind)

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ws, res, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPHeader: headers})

	cancel()

	if res != nil && res.StatusCode == http.StatusUnauthorized {
		return errRefused
	}

	if err != nil {
		return err
	}

	ws.SetReadLimit(readLimit)

	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)

	session, err := yamux.Server(conn, yamuxConfig())
	if err != nil {
		conn.Close()

		return err
	}
	defer session.Close()

	r.Log.Info("relay: connected", "stagd", r.URL, "kind", r.Kind)

	api := &streamListener{streams: make(chan net.Conn), done: make(chan struct{}), addr: conn.LocalAddr()}
	defer api.Close()

	if r.API != nil {
		server := &http.Server{Handler: r.API, ReadHeaderTimeout: 30 * time.Second}

		go server.Serve(api)
		defer server.Close()
	}

	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return err
		}

		go r.handle(ctx, stream, api)
	}
}

func (r *Relay) handle(ctx context.Context, stream *yamux.Stream, api *streamListener) {
	stream.SetReadDeadline(time.Now().Add(30 * time.Second))

	line, err := readLine(stream)
	if err != nil {
		stream.Close()

		return
	}

	stream.SetReadDeadline(time.Time{})

	var h header

	if err := json.Unmarshal([]byte(line), &h); err != nil {
		refuse(stream, "a header the relay doesn't understand")

		return
	}

	switch h.Op {
	case opDial:
		if err := r.Allow(h.Addr); err != nil {
			refuse(stream, err.Error())

			return
		}

		var d net.Dialer

		dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		target, err := d.DialContext(dialCtx, "tcp", h.Addr)

		cancel()

		if err != nil {
			refuse(stream, fmt.Sprintf("can't reach %s: %v", h.Addr, err))

			return
		}

		stream.Write([]byte("ok\n"))
		pipe(stream, target)

	case opAPI:
		if r.API == nil {
			refuse(stream, "this relay doesn't serve an API")

			return
		}

		stream.Write([]byte("ok\n"))
		api.hand(stream)

	default:
		refuse(stream, "unknown stream kind "+h.Op)
	}
}

func refuse(stream net.Conn, why string) {
	stream.Write([]byte("error " + strings.ReplaceAll(why, "\n", " ") + "\n"))
	stream.Close()
}

// streamListener hands API streams to an http.Server as connections.
type streamListener struct {
	streams chan net.Conn
	done    chan struct{}
	once    sync.Once
	addr    net.Addr
}

func (l *streamListener) hand(c net.Conn) {
	select {
	case l.streams <- c:

	case <-l.done:
		c.Close()
	}
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.streams:
		return c, nil

	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *streamListener) Close() error {
	l.once.Do(func() { close(l.done) })

	return nil
}

func (l *streamListener) Addr() net.Addr {
	return l.addr
}
