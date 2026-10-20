// Package relay connects Stagd to compute it can't reach: a cluster without
// a public API server or ingress, a Docker host behind NAT. `stagd relay`
// runs next to the compute and dials out to Stagd; Stagd then opens streams
// back through that one connection, each either
//
//	dial  a TCP connection to a preview's service, e.g. web.stagd-<id>.svc:80
//	api   an HTTP connection to the compute's own API (Kubernetes, Docker),
//	      which the relay calls with its own credentials
//
// The relay decides what may be dialled; Stagd only asks.
//
// The connection is a WebSocket (so it passes proxies that only allow
// HTTPS) carrying yamux streams. Each stream starts with a JSON header line
// from Stagd, then a line from the relay: "ok", or "error <why>".
package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

// Path is where Stagd takes relay connections.
const Path = "/relay/connect"

// The relay says what it is when it connects.
const (
	HeaderVersion = "X-Stagd-Relay-Version"
	HeaderKind    = "X-Stagd-Relay-Kind"
)

// Stream kinds.
const (
	opDial = "dial"
	opAPI  = "api"
)

type header struct {
	Op   string `json:"op"`
	Addr string `json:"addr,omitempty"`
}

// readLimit is the largest WebSocket message: yamux writes up to its window in one.
const readLimit = 4 << 20

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 30 * time.Second
	cfg.LogOutput = io.Discard

	return cfg
}

// readLine reads up to a newline a byte at a time, so nothing after it is taken.
func readLine(r io.Reader) (string, error) {
	var line []byte

	b := make([]byte, 1)

	for len(line) < 4096 {
		if _, err := io.ReadFull(r, b); err != nil {
			return "", err
		}

		if b[0] == '\n' {
			return string(line), nil
		}

		line = append(line, b[0])
	}

	return "", errors.New("relay: header line too long")
}

// open starts a stream: the header out, the relay's answer back.
func open(session *yamux.Session, h header) (net.Conn, error) {
	stream, err := session.OpenStream()
	if err != nil {
		return nil, err
	}

	raw, _ := json.Marshal(h)

	if _, err := stream.Write(append(raw, '\n')); err != nil {
		stream.Close()

		return nil, err
	}

	stream.SetReadDeadline(time.Now().Add(30 * time.Second))

	answer, err := readLine(stream)
	if err != nil {
		stream.Close()

		return nil, fmt.Errorf("relay: no answer: %w", err)
	}

	stream.SetReadDeadline(time.Time{})

	if answer != "ok" {
		stream.Close()

		return nil, fmt.Errorf("relay: %s", strings.TrimPrefix(answer, "error "))
	}

	return stream, nil
}

// pipe copies both ways until either side is done.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)

	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		done <- struct{}{}
	}

	go cp(a, b)
	go cp(b, a)

	<-done

	a.Close()
	b.Close()
}
