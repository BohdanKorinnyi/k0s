package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	cerrdefs "github.com/containerd/errdefs"

	"dockpanel/internal/services"
	"dockpanel/internal/streams"
)

const (
	defaultTail = "500"
	maxTail     = 100_000

	// A batch of log lines is sent when it reaches either size, or after the
	// interval: few large frames instead of one per line, without a
	// noticeable delay.
	logBatchLines = 200
	logBatchBytes = 1 << 20
	logFlushEvery = 50 * time.Millisecond

	wsWriteTimeout = 15 * time.Second
	// The most the browser may send in one frame on a shell connection; a
	// large paste arrives as several.
	execReadLimit = 1 << 20
)

// parseTail reads the ?tail= parameter: "all", or a number of lines.
func parseTail(raw string) (string, bool) {
	switch raw {
	case "":
		return defaultTail, true
	case "all":
		return "all", true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxTail || strconv.Itoa(n) != raw {
		return "", false
	}
	return raw, true
}

// handleContainerLogsWS streams one container's log.
func (s *server) handleContainerLogsWS(w http.ResponseWriter, r *http.Request) {
	tail, ok := parseTail(r.URL.Query().Get("tail"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid", `tail must be "all" or a number from 1 to 100000`)
		return
	}
	cli, c, ok := s.inspectFor(w, r)
	if !ok {
		return
	}
	name := strings.TrimPrefix(c.Name, "/")
	s.streamLogs(w, r, func(ctx context.Context, emit func(streams.LogLine)) error {
		return streams.ReadLogs(ctx, cli, c.ID, name, streams.LogOptions{Tail: tail, Follow: true}, emit)
	})
}

// handleServiceLogsWS streams the logs of all of a service's containers as one.
func (s *server) handleServiceLogsWS(w http.ResponseWriter, r *http.Request) {
	tail, ok := parseTail(r.URL.Query().Get("tail"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid", `tail must be "all" or a number from 1 to 100000`)
		return
	}
	h, cli, ok := s.clientFor(w, r)
	if !ok {
		return
	}
	groups, ok := s.listServices(w, r, h, cli)
	if !ok {
		return
	}
	svc, found := services.Find(groups, r.PathValue("key"))
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "no such service on "+h.Name)
		return
	}
	s.streamLogs(w, r, func(ctx context.Context, emit func(streams.LogLine)) error {
		return streams.MergedLogs(ctx, cli, svc.Containers, streams.MergeOptions{Tail: tail, Follow: true}, emit)
	})
}

// streamLogs upgrades the request to a WebSocket and sends what read emits,
// as text frames each holding a JSON array of lines.
func (s *server) streamLogs(w http.ResponseWriter, r *http.Request, read func(ctx context.Context, emit func(streams.LogLine)) error) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has answered
	}
	defer conn.CloseNow()

	// The browser sends nothing on this socket; CloseRead notices when it
	// goes away and ends the context, which stops the log reader.
	ctx, cancel := context.WithCancel(conn.CloseRead(r.Context()))
	defer cancel()

	batch := &lineBatcher{conn: conn, cancel: cancel}
	go batch.run(ctx)

	err = read(ctx, batch.add)
	batch.flush()
	if err != nil && ctx.Err() == nil {
		slog.Warn("log stream failed", "path", r.URL.Path, "error", err)
		_ = conn.Close(websocket.StatusInternalError, closeReason(err))
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "end of log")
}

// lineBatcher gathers log lines and writes them to the socket in batches.
type lineBatcher struct {
	conn   *websocket.Conn
	cancel context.CancelFunc

	mu      sync.Mutex
	pending []streams.LogLine
	size    int
}

func (b *lineBatcher) add(l streams.LogLine) {
	b.mu.Lock()
	b.pending = append(b.pending, l)
	b.size += len(l.Text) + 64
	full := len(b.pending) >= logBatchLines || b.size >= logBatchBytes
	b.mu.Unlock()
	if full {
		b.flush()
	}
}

func (b *lineBatcher) flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return
	}
	data, err := json.Marshal(b.pending)
	b.pending, b.size = nil, 0
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()
	if err := b.conn.Write(ctx, websocket.MessageText, data); err != nil {
		b.cancel() // the browser is gone or too slow: stop reading the log
	}
}

func (b *lineBatcher) run(ctx context.Context) {
	ticker := time.NewTicker(logFlushEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.flush()
		}
	}
}

// handleLogsDownload sends a container's whole log as a text file.
func (s *server) handleLogsDownload(w http.ResponseWriter, r *http.Request) {
	cli, c, ok := s.inspectFor(w, r)
	if !ok {
		return
	}
	name := strings.TrimPrefix(c.Name, "/")
	withTimes := r.URL.Query().Get("timestamps") == "1"

	// Container names hold only letters, digits and . _ - so the name is safe
	// inside the quoted file name.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.log"`)
	w.Header().Set("Cache-Control", "no-store")

	out := bufio.NewWriterSize(w, 64*1024)
	startOfLine := true
	err := streams.ReadLogs(r.Context(), cli, c.ID, name, streams.LogOptions{Tail: "all"}, func(l streams.LogLine) {
		if startOfLine && withTimes && l.Time != "" {
			out.WriteString(l.Time)
			out.WriteByte(' ')
		}
		out.WriteString(l.Text)
		// An over-long line arrives in pieces; rejoin them in the file.
		startOfLine = !l.Partial
		if startOfLine {
			out.WriteByte('\n')
		}
	})
	if flushErr := out.Flush(); err == nil {
		err = flushErr
	}
	if err != nil && r.Context().Err() == nil {
		// The headers are sent; all that can be done is to say so in the log.
		slog.Warn("log download cut short", "container", name, "error", err)
	}
}

// execControl is a control message on a shell connection. Terminal bytes
// travel as binary frames; these travel as text frames.
type execControl struct {
	Type string `json:"type"` // "resize" from the browser, "exit" from the server
	Cols uint   `json:"cols,omitempty"`
	Rows uint   `json:"rows,omitempty"`
	// Code and Message describe how a session ended.
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleExecWS runs a shell in a container, connected to a terminal in the
// browser. Closing the socket ends the shell.
func (s *server) handleExecWS(w http.ResponseWriter, r *http.Request) {
	_, cli, ok := s.clientFor(w, r)
	if !ok {
		return
	}
	ref := r.PathValue("id")
	if !validContainerRef(ref) {
		writeError(w, http.StatusBadRequest, "invalid", "not a container name or ID")
		return
	}
	cols, rows := uintParam(r, "cols"), uintParam(r, "rows")

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(execReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// A browser cannot read the body of a failed WebSocket handshake, so
	// problems are reported over the open socket, then it is closed.
	sendExit := func(code int, message string) {
		data, _ := json.Marshal(execControl{Type: "exit", Code: code, Message: message})
		wctx, done := context.WithTimeout(context.Background(), wsWriteTimeout)
		defer done()
		_ = conn.Write(wctx, websocket.MessageText, data)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}

	session, err := streams.StartShell(ctx, cli, ref, cols, rows)
	if err != nil {
		sendExit(-1, shellProblem(err))
		return
	}
	defer session.Close()

	// Shell output → browser.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := session.Read(buf)
			if n > 0 {
				wctx, done := context.WithTimeout(context.Background(), wsWriteTimeout)
				werr := conn.Write(wctx, websocket.MessageBinary, buf[:n])
				done()
				if werr != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}
		// The shell ended; say how.
		ictx, done := context.WithTimeout(context.Background(), pingTimeout)
		defer done()
		code, err := session.ExitCode(ictx)
		message := ""
		if err != nil {
			message = shellProblem(err)
		}
		sendExit(code, message)
	}()

	// Browser → shell: keystrokes as binary frames, control as text frames.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := session.Write(data); err != nil {
				return
			}
			continue
		}
		var c execControl
		if json.Unmarshal(data, &c) == nil && c.Type == "resize" {
			_ = session.Resize(ctx, c.Cols, c.Rows)
		}
	}
}

// shellProblem words a failure to open or run a shell for the person at the
// terminal.
func shellProblem(err error) string {
	switch {
	case errors.Is(err, streams.ErrNoShell):
		return "this container has no shell: its image does not include sh or bash"
	case errors.Is(err, streams.ErrNotRunning):
		return "the container is not running"
	case cerrdefs.IsNotFound(err):
		return "the container no longer exists"
	}
	return brief(err)
}

func uintParam(r *http.Request, name string) uint {
	n, err := strconv.ParseUint(r.URL.Query().Get(name), 10, 16)
	if err != nil {
		return 0
	}
	return uint(n)
}

// closeReason fits an error into a WebSocket close frame, which carries at
// most 123 bytes of text.
func closeReason(err error) string {
	msg := brief(err)
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return strings.ToValidUTF8(msg, "")
}
