package streams

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"dockpanel/internal/dockerapi/dockertest"
	"dockpanel/internal/services"
)

var ctx = context.Background()

// frame builds one frame of Docker's two-stream log format: a type byte
// (1 stdout, 2 stderr), three zero bytes, the payload length, the payload.
func frame(stream byte, payload string) []byte {
	head := make([]byte, 8)
	head[0] = stream
	binary.BigEndian.PutUint32(head[4:], uint32(len(payload)))
	return append(head, payload...)
}

func collect(t *testing.T, f *dockertest.Fake, name string, o LogOptions) []LogLine {
	t.Helper()
	var got []LogLine
	if err := ReadLogs(ctx, f, name, name, o, func(l LogLine) { got = append(got, l) }); err != nil {
		t.Fatalf("ReadLogs: %v", err)
	}
	return got
}

func TestSplitTimestamp(t *testing.T) {
	cases := []struct{ raw, ts, text string }{
		{"2026-11-26T10:00:00.123456789Z hello world", "2026-11-26T10:00:00.123456789Z", "hello world"},
		{"2026-11-26T10:00:00Z short stamp", "2026-11-26T10:00:00Z", "short stamp"},
		{"2026-11-26T10:00:00.123456789+02:00 offset", "2026-11-26T10:00:00.123456789+02:00", "offset"},
		{"no timestamp", "", "no timestamp"},
		{"2026-11-26T10:00:00.123456789Z", "2026-11-26T10:00:00.123456789Z", ""},
		{"2026-11-26T10:00:00.123456789Z ", "2026-11-26T10:00:00.123456789Z", ""},
		{"2026-11-26T10:00:00.123456789Z   indented", "2026-11-26T10:00:00.123456789Z", "  indented"},
		{"", "", ""},
		{"2026-99-99T10:00:00Z looks like one", "", "2026-99-99T10:00:00Z looks like one"},
	}
	for _, c := range cases {
		ts, text := splitTimestamp(c.raw)
		if ts != c.ts || text != c.text {
			t.Errorf("splitTimestamp(%q) = %q, %q; want %q, %q", c.raw, ts, text, c.ts, c.text)
		}
	}
}

func TestReadLogsStreams(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	var raw []byte
	raw = append(raw, frame(1, "2026-11-26T10:00:00.000000001Z started\n")...)
	raw = append(raw, frame(2, "2026-11-26T10:00:01.000000002Z something failed\n")...)
	f.SetLogs("api-1", raw)

	got := collect(t, f, "api-1", LogOptions{Tail: "500"})
	want := []LogLine{
		{Container: "api-1", Stream: "stdout", Time: "2026-11-26T10:00:00.000000001Z", Text: "started"},
		{Container: "api-1", Stream: "stderr", Time: "2026-11-26T10:00:01.000000002Z", Text: "something failed"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %+v\nwant    %+v", got, want)
	}
}

// A line may arrive split across frames, and one frame may hold several lines.
func TestReadLogsFrameBoundaries(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	var raw []byte
	raw = append(raw, frame(1, "2026-11-26T10:00:00Z first half, ")...)
	raw = append(raw, frame(1, "second half\n2026-11-26T10:00:01Z two\r\n2026-11-26T10:00:02Z no newline at the end")...)
	f.SetLogs("api-1", raw)

	var texts []string
	for _, l := range collect(t, f, "api-1", LogOptions{}) {
		texts = append(texts, l.Text)
	}
	want := []string{"first half, second half", "two", "no newline at the end"}
	if !reflect.DeepEqual(texts, want) {
		t.Errorf("texts = %q, want %q", texts, want)
	}
}

func TestReadLogsTTY(t *testing.T) {
	f := dockertest.New()
	c := dockertest.Running("tty-1", "app:1")
	c.Config.Tty = true
	f.AddContainer(c)
	// A container with a terminal has one raw stream, no framing.
	f.SetLogs("tty-1", []byte("2026-11-26T10:00:00Z one\r\n2026-11-26T10:00:01Z two\r\n"))

	got := collect(t, f, "tty-1", LogOptions{})
	if len(got) != 2 || got[0].Text != "one" || got[1].Text != "two" || got[0].Stream != "stdout" {
		t.Errorf("lines = %+v", got)
	}
}

func TestReadLogsHugeLine(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	huge := strings.Repeat("x", 600_000)
	f.SetLogs("api-1", append(frame(1, "2026-11-26T10:00:00Z "+huge+"\n"), frame(1, "2026-11-26T10:00:01Z after\n")...))

	got := collect(t, f, "api-1", LogOptions{})
	if len(got) < 4 {
		t.Fatalf("got %d lines, want the huge line in at least 3 pieces plus the next line", len(got))
	}
	var rebuilt strings.Builder
	for _, l := range got[:len(got)-1] {
		if len(l.Text) > MaxLine {
			t.Errorf("a piece is %d bytes, over MaxLine", len(l.Text))
		}
		if l.Time != "2026-11-26T10:00:00Z" {
			t.Errorf("piece time = %q, want every piece to carry the line's time", l.Time)
		}
		rebuilt.WriteString(l.Text)
	}
	if rebuilt.String() != huge {
		t.Errorf("the pieces do not add up to the original line (%d bytes of %d)", rebuilt.Len(), len(huge))
	}
	if last := got[len(got)-1]; last.Text != "after" || last.Time != "2026-11-26T10:00:01Z" {
		t.Errorf("line after the huge one = %+v", last)
	}
}

func TestReadLogsBinary(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	f.SetLogs("api-1", frame(1, "2026-11-26T10:00:00Z bad \xff\xfe bytes \x00 here\n"))

	got := collect(t, f, "api-1", LogOptions{})
	if len(got) != 1 || !utf8.ValidString(got[0].Text) {
		t.Fatalf("lines = %+v, want one line of valid UTF-8", got)
	}
	if !strings.HasPrefix(got[0].Text, "bad ") || !strings.HasSuffix(got[0].Text, " here") {
		t.Errorf("text = %q, want the readable parts kept", got[0].Text)
	}
}

func TestReadLogsPassesOptions(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	collect(t, f, "api-1", LogOptions{Tail: "100", Follow: true, Since: "2026-11-26T10:00:00Z"})
	o := f.LastLogs()
	if !o.ShowStdout || !o.ShowStderr || !o.Timestamps || !o.Follow || o.Tail != "100" || o.Since != "2026-11-26T10:00:00Z" {
		t.Errorf("options sent to Docker = %+v", o)
	}
}

func TestReadLogsErrors(t *testing.T) {
	f := dockertest.New()
	if err := ReadLogs(ctx, f, "nope", "nope", LogOptions{}, func(LogLine) {}); err == nil {
		t.Error("want an error for a container that does not exist")
	}
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	boom := errors.New("driver does not support reading logs")
	f.Fail("ContainerLogs", boom)
	if err := ReadLogs(ctx, f, "api-1", "api-1", LogOptions{}, func(LogLine) {}); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the Docker error", err)
	}
}

func TestMergedBacklogSorted(t *testing.T) {
	f := dockertest.New()
	a := f.AddContainer(dockertest.Running("a", "app:1"))
	b := f.AddContainer(dockertest.Running("b", "app:1"))
	f.SetLogs("a", append(
		frame(1, "2026-11-26T10:00:01.000000000Z a-first\n"),
		frame(1, "2026-11-26T10:00:03.000000000Z a-second\n")...))
	f.SetLogs("b", frame(2, "2026-11-26T10:00:02.000000000Z b-only\n"))

	var got []string
	containers := []services.Container{{ID: a, Name: "a"}, {ID: b, Name: "b"}}
	err := MergedLogs(ctx, f, containers, MergeOptions{Tail: "500"}, func(l LogLine) {
		got = append(got, l.Container+":"+l.Stream+":"+l.Text)
	})
	if err != nil {
		t.Fatalf("MergedLogs: %v", err)
	}
	want := []string{"a:stdout:a-first", "b:stderr:b-only", "a:stdout:a-second"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged = %q, want %q", got, want)
	}
}

func TestMergedBacklogKeepsTheNewestLines(t *testing.T) {
	f := dockertest.New()
	a := f.AddContainer(dockertest.Running("a", "app:1"))
	b := f.AddContainer(dockertest.Running("b", "app:1"))
	f.SetLogs("a", append(
		frame(1, "2026-11-26T10:00:01Z a1\n"),
		frame(1, "2026-11-26T10:00:04Z a2\n")...))
	f.SetLogs("b", append(
		frame(1, "2026-11-26T10:00:02Z b1\n"),
		frame(1, "2026-11-26T10:00:03Z b2\n")...))

	var got []string
	containers := []services.Container{{ID: a, Name: "a"}, {ID: b, Name: "b"}}
	if err := MergedLogs(ctx, f, containers, MergeOptions{Tail: "2"}, func(l LogLine) { got = append(got, l.Text) }); err != nil {
		t.Fatal(err)
	}
	if want := []string{"b2", "a2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("merged with tail 2 = %q, want the two newest %q", got, want)
	}
}

func TestMergedFollowSkipsWhatTheBacklogAlreadyShowed(t *testing.T) {
	f := dockertest.New()
	a := f.AddContainer(dockertest.Running("a", "app:1"))
	// The fake returns the same bytes for the backlog and for the follow
	// request, as Docker does for the line at the "since" boundary.
	f.SetLogs("a", append(
		frame(1, "2026-11-26T10:00:01Z one\n"),
		frame(1, "2026-11-26T10:00:02Z two\n")...))

	var got []string
	err := MergedLogs(ctx, f, []services.Container{{ID: a, Name: "a"}}, MergeOptions{Tail: "500", Follow: true}, func(l LogLine) {
		got = append(got, l.Text)
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want each once: %q", got, want)
	}
	if o := f.LastLogs(); !o.Follow || o.Since != "2026-11-26T10:00:02Z" {
		t.Errorf("follow request = %+v, want it to follow since the last backlog line", o)
	}
}

func TestMergedLogsError(t *testing.T) {
	f := dockertest.New()
	a := f.AddContainer(dockertest.Running("a", "app:1"))
	boom := errors.New("cannot read logs")
	f.Fail("ContainerLogs", boom)
	err := MergedLogs(ctx, f, []services.Container{{ID: a, Name: "a"}}, MergeOptions{}, func(LogLine) {})
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want the Docker error", err)
	}
}

func TestLineWriterEmitsNothingForEmptyInput(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("quiet", "app:1"))
	f.SetLogs("quiet", bytes.Repeat(nil, 0))
	if got := collect(t, f, "quiet", LogOptions{}); len(got) != 0 {
		t.Errorf("lines = %+v, want none", got)
	}
}
