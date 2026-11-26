package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"dc0s/internal/dockerapi/dockertest"
	"dc0s/internal/streams"
)

// frame builds one frame of Docker's two-stream log format.
func frame(stream byte, payload string) []byte {
	head := make([]byte, 8)
	head[0] = stream
	binary.BigEndian.PutUint32(head[4:], uint32(len(payload)))
	return append(head, payload...)
}

// serve starts a real HTTP server for the rig, which WebSockets need.
func (r *rig) serve() *httptest.Server {
	srv := httptest.NewServer(r.h)
	r.t.Cleanup(srv.Close)
	return srv
}

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + path
}

func dial(t *testing.T, srv *httptest.Server, path string, opts *websocket.DialOptions) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL(srv, path), opts)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (HTTP %d)", path, err, status)
	}
	conn.SetReadLimit(64 << 20)
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

// dialStatus returns the HTTP status of a handshake that is expected to fail.
func dialStatus(t *testing.T, srv *httptest.Server, path string, opts *websocket.DialOptions) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, wsURL(srv, path), opts)
	if err == nil {
		conn.CloseNow()
		t.Fatalf("dial %s succeeded, want it refused", path)
	}
	if resp == nil {
		t.Fatalf("dial %s: %v (no HTTP response)", path, err)
	}
	return resp.StatusCode
}

// readLines reads log batches until want lines have arrived or the socket closes.
func readLines(t *testing.T, conn *websocket.Conn, want int) []streams.LogLine {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var lines []streams.LogLine
	for len(lines) < want {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if typ != websocket.MessageText {
			t.Fatalf("log frame type = %v, want text", typ)
		}
		var batch []streams.LogLine
		if err := json.Unmarshal(data, &batch); err != nil {
			t.Fatalf("log frame is not a JSON array of lines: %v: %s", err, data)
		}
		lines = append(lines, batch...)
	}
	return lines
}

func TestLogsWS(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	f.SetLogs("shop-api-1", append(
		frame(1, "2026-11-26T10:00:00.000000001Z started\n"),
		frame(2, "2026-11-26T10:00:01.000000002Z <script>alert(1)</script>\n")...))
	srv := r.serve()
	base := "/api/hosts/" + h.ID + "/containers/shop-api-1/logs/ws"

	lines := readLines(t, dial(t, srv, base+"?tail=100", nil), 2)
	want := []streams.LogLine{
		{Container: "shop-api-1", Stream: "stdout", Time: "2026-11-26T10:00:00.000000001Z", Text: "started"},
		{Container: "shop-api-1", Stream: "stderr", Time: "2026-11-26T10:00:01.000000002Z", Text: "<script>alert(1)</script>"},
	}
	if !slices.Equal(lines, want) {
		t.Errorf("lines = %+v\nwant    %+v", lines, want)
	}
	if o := f.LastLogs(); o.Tail != "100" || !o.Follow {
		t.Errorf("log request = %+v, want tail 100 and follow", o)
	}

	// The default is the last 500 lines.
	readLines(t, dial(t, srv, base, nil), 2)
	if o := f.LastLogs(); o.Tail != "500" {
		t.Errorf("default tail = %q, want 500", o.Tail)
	}
	readLines(t, dial(t, srv, base+"?tail=all", nil), 2)
	if o := f.LastLogs(); o.Tail != "all" {
		t.Errorf("tail = %q, want all", o.Tail)
	}

	for _, bad := range []string{"abc", "0", "-5", "100001", "1e3"} {
		if status := dialStatus(t, srv, base+"?tail="+bad, nil); status != http.StatusBadRequest {
			t.Errorf("tail=%s: HTTP %d, want 400", bad, status)
		}
	}
	if status := dialStatus(t, srv, "/api/hosts/"+h.ID+"/containers/nope/logs/ws", nil); status != http.StatusNotFound {
		t.Errorf("unknown container: HTTP %d, want 404", status)
	}
	if status := dialStatus(t, srv, "/api/hosts/nope/containers/shop-api-1/logs/ws", nil); status != http.StatusNotFound {
		t.Errorf("unknown host: HTTP %d, want 404", status)
	}
}

func TestLiveConnectionsRefuseOtherSites(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	srv := r.serve()
	evil := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://evil.example"}}}

	for _, path := range []string{
		"/api/hosts/" + h.ID + "/containers/shop-api-1/logs/ws",
		"/api/hosts/" + h.ID + "/services/c~shop~api/logs/ws",
		"/api/hosts/" + h.ID + "/containers/shop-api-1/exec/ws",
	} {
		if status := dialStatus(t, srv, path, evil); status != http.StatusForbidden {
			t.Errorf("%s from another site: HTTP %d, want 403", path, status)
		}
	}
	if calls := f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q: a shell was opened for another site", calls)
	}

	// The same origin is welcome.
	u := strings.TrimPrefix(srv.URL, "http://")
	same := &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://" + u}}}
	dial(t, srv, "/api/hosts/"+h.ID+"/containers/shop-api-1/logs/ws", same)
}

func TestLiveConnectionsNeedLogin(t *testing.T) {
	r := newRig(t, func(d *Deps) { d.Config.Password = "s3cret" })
	h, f := r.addHost("aws", false)
	shop(f)
	srv := r.serve()
	for _, path := range []string{
		"/api/hosts/" + h.ID + "/containers/shop-api-1/logs/ws",
		"/api/hosts/" + h.ID + "/containers/shop-api-1/exec/ws",
		"/api/hosts/" + h.ID + "/containers/shop-api-1/logs/download",
	} {
		if strings.HasSuffix(path, "/download") {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s without login: HTTP %d, want 401", path, resp.StatusCode)
			}
			continue
		}
		if status := dialStatus(t, srv, path, nil); status != http.StatusUnauthorized {
			t.Errorf("%s without login: HTTP %d, want 401", path, status)
		}
	}
}

func TestMergedLogsWS(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	f.SetLogs("shop-api-1", append(
		frame(1, "2026-11-26T10:00:01Z one-a\n"),
		frame(1, "2026-11-26T10:00:03Z one-b\n")...))
	f.SetLogs("shop-api-2", frame(1, "2026-11-26T10:00:02Z two-a\n"))
	srv := r.serve()

	lines := readLines(t, dial(t, srv, "/api/hosts/"+h.ID+"/services/c~shop~api/logs/ws", nil), 3)
	var got []string
	for _, l := range lines {
		got = append(got, l.Container+":"+l.Text)
	}
	want := []string{"shop-api-1:one-a", "shop-api-2:two-a", "shop-api-1:one-b"}
	if !slices.Equal(got, want) {
		t.Errorf("merged = %q, want %q", got, want)
	}
	if status := dialStatus(t, srv, "/api/hosts/"+h.ID+"/services/c~shop~nope/logs/ws", nil); status != http.StatusNotFound {
		t.Errorf("unknown service: HTTP %d, want 404", status)
	}
}

func TestLogBatchesStaySmall(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	var raw []byte
	for i := 0; i < 1000; i++ {
		raw = append(raw, frame(1, "2026-11-26T10:00:00Z line\n")...)
	}
	f.SetLogs("shop-api-1", raw)
	srv := r.serve()
	conn := dial(t, srv, "/api/hosts/"+h.ID+"/containers/shop-api-1/logs/ws?tail=all", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	total := 0
	for total < 1000 {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("after %d lines: %v", total, err)
		}
		var batch []streams.LogLine
		if err := json.Unmarshal(data, &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch) > 200 {
			t.Fatalf("a batch holds %d lines, want at most 200", len(batch))
		}
		total += len(batch)
	}
}

func TestDownload(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	f.SetLogs("shop-api-1", append(
		frame(1, "2026-11-26T10:00:00.000000001Z started\n"),
		frame(2, "2026-11-26T10:00:01.000000002Z failed\n")...))
	base := "/api/hosts/" + h.ID + "/containers/shop-api-1/logs/download"

	rec := r.do("GET", base, nil)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="shop-api-1.log"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if got := rec.Body.String(); got != "started\nfailed\n" {
		t.Errorf("body = %q", got)
	}
	if o := f.LastLogs(); o.Follow || o.Tail != "all" {
		t.Errorf("log request = %+v, want the whole log without following", o)
	}

	rec = r.do("GET", base+"?timestamps=1", nil)
	want := "2026-11-26T10:00:00.000000001Z started\n2026-11-26T10:00:01.000000002Z failed\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("body with timestamps = %q\nwant                 %q", got, want)
	}
	wantError(t, r.do("GET", "/api/hosts/"+h.ID+"/containers/nope/logs/download", nil), http.StatusNotFound, "not_found")
}

type control struct {
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// readUntilExit collects terminal output until the exit control frame.
func readUntilExit(t *testing.T, conn *websocket.Conn) (output string, exit control) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out strings.Builder
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("socket ended without an exit frame (output so far %q): %v", out.String(), err)
		}
		if typ == websocket.MessageBinary {
			out.Write(data)
			continue
		}
		var c control
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatalf("control frame %q: %v", data, err)
		}
		if c.Type == "exit" {
			return out.String(), c
		}
	}
}

func TestExecWS(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	srv := r.serve()
	conn := dial(t, srv, "/api/hosts/"+h.ID+"/containers/shop-api-1/exec/ws?cols=120&rows=40", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := conn.Write(ctx, websocket.MessageBinary, []byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(data) != "echo hi\n" {
		t.Fatalf("read %v %q, %v; want the fake shell's echo as a binary frame", typ, data, err)
	}
	if size := f.LastExec().ConsoleSize; size.Width != 120 || size.Height != 40 {
		t.Errorf("terminal size = %+v, want 120x40", size)
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":132,"rows":50}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !slices.Contains(f.CallLog(), "ExecResize 132x50") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Contains(f.CallLog(), "ExecResize 132x50") {
		t.Errorf("calls = %q, want ExecResize 132x50", f.CallLog())
	}

	// Garbage on the control channel must not end the session.
	if err := conn.Write(ctx, websocket.MessageText, []byte(`not json`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("still here\n")); err != nil {
		t.Fatal(err)
	}
	if _, data, err := conn.Read(ctx); err != nil || string(data) != "still here\n" {
		t.Errorf("after a bad control frame: read %q, %v", data, err)
	}
}

func TestExecWSShellExits(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	f.ExecHandler = func(conn net.Conn) {
		_, _ = io.WriteString(conn, "bye\r\n")
		conn.Close()
	}
	srv := r.serve()
	conn := dial(t, srv, "/api/hosts/"+h.ID+"/containers/shop-api-1/exec/ws", nil)

	out, exit := readUntilExit(t, conn)
	if out != "bye\r\n" || exit.Code != 0 || exit.Message != "" {
		t.Errorf("output %q, exit %+v; want the output then a clean exit", out, exit)
	}
}

func TestExecWSNoShell(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	f.ExecExitCode = 127
	f.ExecHandler = func(conn net.Conn) {
		_, _ = io.WriteString(conn, `OCI runtime exec failed: exec: "/bin/sh": no such file or directory`)
		conn.Close()
	}
	srv := r.serve()
	conn := dial(t, srv, "/api/hosts/"+h.ID+"/containers/shop-api-1/exec/ws", nil)

	_, exit := readUntilExit(t, conn)
	if exit.Code != 127 || !strings.Contains(exit.Message, "no shell") {
		t.Errorf("exit = %+v, want code 127 and a message saying there is no shell", exit)
	}
}

func TestExecWSNotRunning(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	f.AddContainer(dockertest.Stopped("job", "app:1", 0))
	srv := r.serve()
	conn := dial(t, srv, "/api/hosts/"+h.ID+"/containers/job/exec/ws", nil)

	_, exit := readUntilExit(t, conn)
	if !strings.Contains(exit.Message, "not running") {
		t.Errorf("exit = %+v, want a message saying the container is not running", exit)
	}
	if calls := f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none", calls)
	}
}

func TestClosingTheSocketEndsTheShell(t *testing.T) {
	r := newRig(t, nil)
	h, f := r.addHost("aws", false)
	shop(f)
	ended := make(chan struct{})
	f.ExecHandler = func(conn net.Conn) {
		defer close(ended)
		_, _ = io.Copy(io.Discard, conn) // returns when the session is closed
	}
	srv := r.serve()
	conn := dial(t, srv, "/api/hosts/"+h.ID+"/containers/shop-api-1/exec/ws", nil)
	conn.Close(websocket.StatusNormalClosure, "")

	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Error("the shell session was left open after the browser closed the socket")
	}
}
