package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v3"
)

// mcpPath is where Stagd's server answers MCP (the platform's
// workers/server/src/mcp): the tools live there, so a new one needs no new
// CLI.
const mcpPath = "/mcp"

func mcpCommand() *cli.Command {
	return &cli.Command{
		Name:  "mcp",
		Usage: "Stagd's tools for coding agents, as an MCP server over stdio",
		Description: "For Claude Code, Cursor and other MCP clients: they start it, and it hands each call to Stagd,\n" +
			"signed in as you (stagd login). The tools: make a preview of a pull request or branch, wait for it,\n" +
			"look at it (its screenshot, logs and mail), and check stagd.yaml before pushing.\n\n" +
			"   claude mcp add stagd -- stagd mcp\n\n" +
			"or in an mcp.json: { \"mcpServers\": { \"stagd\": { \"command\": \"stagd\", \"args\": [\"mcp\"] } } }",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			t := token(cmd)

			if t == "" {
				return errors.New("not signed in: run `stagd login` first, then start `stagd mcp` again")
			}

			b := &mcpBridge{
				endpoint:  strings.TrimRight(cmd.String("api-url"), "/") + mcpPath,
				token:     t,
				userAgent: "stagd-cli/" + cmd.Root().Version,
				http:      &http.Client{Timeout: 3 * time.Minute},
				out:       cmd.Root().Writer,
				log:       cmd.Root().ErrWriter,
			}

			return b.run(ctx, cmd.Root().Reader)
		},
	}
}

// mcpBridge carries an MCP client's stdio to Stagd's /mcp: each message on
// its own line in, each POSTed as it comes, each answer written as a line.
// Calls run side by side (a ping needn't wait behind a wait_for_preview);
// answers are written one at a time.
type mcpBridge struct {
	endpoint  string
	token     string
	userAgent string
	http      *http.Client
	out       io.Writer
	log       io.Writer

	mu sync.Mutex
}

// rpcMessage is as much of a JSON-RPC message as the bridge reads: its id, to
// answer a call that couldn't reach Stagd.
type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
}

func (b *mcpBridge) run(ctx context.Context, in io.Reader) error {
	if in == nil {
		return errors.New("no standard input to read MCP messages from")
	}

	if b.log == nil {
		b.log = io.Discard
	}

	reader := bufio.NewReaderSize(in, 1<<20)

	var calls sync.WaitGroup

	defer calls.Wait()

	for {
		line, err := reader.ReadBytes('\n')

		if msg := bytes.TrimSpace(line); len(msg) > 0 {
			calls.Add(1)

			go func() {
				defer calls.Done()

				b.forward(ctx, msg)
			}()
		}

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("reading MCP messages: %w", err)
		}
	}
}

// forward POSTs one message to /mcp and writes what comes back. A call
// (a message with an id) always gets an answer: Stagd's, or an error saying
// why there's none.
func (b *mcpBridge) forward(ctx context.Context, msg []byte) {
	var m rpcMessage

	if err := json.Unmarshal(msg, &m); err != nil {
		b.reply(nil, -32700, "not a JSON-RPC message: "+err.Error())

		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(msg))
	if err != nil {
		b.reply(m.ID, -32603, err.Error())

		return
	}

	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("User-Agent", b.userAgent)

	res, err := b.http.Do(req)
	if err != nil {
		fmt.Fprintf(b.log, "stagd mcp: %s: %v\n", m.Method, err)
		b.reply(m.ID, -32603, "couldn't reach Stagd: "+err.Error())

		return
	}

	defer res.Body.Close()

	switch {
	case res.StatusCode == http.StatusAccepted:
		// A notification or a response: nothing comes back.
		return

	case res.StatusCode == http.StatusUnauthorized:
		fmt.Fprintln(b.log, "stagd mcp: Stagd didn't take the sign-in: run `stagd login`")
		b.reply(m.ID, -32001, "not signed in to Stagd: run `stagd login`, then restart the MCP server")

		return

	case strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream"):
		b.events(res.Body)

		return

	case res.StatusCode >= 300:
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))

		// A JSON-RPC error in the body says it best; else the status.
		if json.Valid(body) && bytes.Contains(body, []byte(`"jsonrpc"`)) {
			b.write(body)

			return
		}

		b.reply(m.ID, -32603, fmt.Sprintf("Stagd answered %s: %s", res.Status, strings.TrimSpace(string(body))))

		return
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		b.reply(m.ID, -32603, "reading Stagd's answer: "+err.Error())

		return
	}

	b.write(body)
}

// events writes each message of an event stream's answer, should the server send one.
func (b *mcpBridge) events(body io.Reader) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)

	var data []string

	flush := func() {
		if len(data) > 0 {
			b.write([]byte(strings.Join(data, "\n")))
			data = nil
		}
	}

	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}

	flush()
}

// reply answers a call that got no answer from Stagd; a notification gets none.
func (b *mcpBridge) reply(id json.RawMessage, code int, message string) {
	if len(id) == 0 || string(id) == "null" {
		return
	}

	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})

	b.write(body)
}

// write puts one message on stdout, on one line, as MCP's stdio wants.
func (b *mcpBridge) write(msg []byte) {
	var line bytes.Buffer

	if err := json.Compact(&line, msg); err != nil {
		fmt.Fprintf(b.log, "stagd mcp: Stagd's answer isn't JSON: %v\n", err)

		return
	}

	line.WriteByte('\n')

	b.mu.Lock()
	defer b.mu.Unlock()

	_, _ = b.out.Write(line.Bytes())
}
