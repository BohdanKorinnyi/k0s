// Package streams reads the two live things a container offers: its log, and
// an interactive shell inside it.
package streams

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"dc0s/internal/dockerapi"
	"dc0s/internal/services"
)

// LogLine is one line of a container's log. The JSON keys are short because a
// busy log sends a great many of these to the browser.
type LogLine struct {
	Container string `json:"c"`
	Stream    string `json:"s"` // "stdout" or "stderr"
	// Time is as Docker reports it: RFC 3339 with nanoseconds. Docker is always
	// asked for timestamps; whether to show them is the browser's choice.
	Time string `json:"t"`
	Text string `json:"l"`
	// Partial is set when the line was too long for one LogLine and continues
	// in the next one.
	Partial bool `json:"p,omitempty"`
}

// LogOptions select what to read.
type LogOptions struct {
	// Tail is how many lines from the end to start with: a number, or "all".
	// Empty means "all".
	Tail string
	// Follow keeps reading new lines until the context ends or the container
	// stops.
	Follow bool
	// Since limits the log to lines at or after a time (RFC 3339).
	Since string
}

// MaxLine is the most text one LogLine carries. A longer line is delivered as
// several LogLines with the same time, so one runaway line cannot stall the
// reader or the browser.
const MaxLine = 256 * 1024

// maxStamp is more than the longest timestamp Docker writes before a line.
const maxStamp = 64

// ReadLogs reads a container's log and calls emit for each line, in order,
// from a single goroutine. It returns when the log ends, or, when following,
// when the context ends.
func ReadLogs(ctx context.Context, cli dockerapi.Client, containerID, name string, o LogOptions, emit func(LogLine)) error {
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	tty := inspected.Container.Config != nil && inspected.Container.Config.Tty

	tail := o.Tail
	if tail == "" {
		tail = "all"
	}
	rc, err := cli.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Follow:     o.Follow,
		Tail:       tail,
		Since:      o.Since,
	})
	if err != nil {
		return err
	}
	defer rc.Close()

	out := &lineSplitter{container: name, stream: "stdout", emit: emit}
	if tty {
		// A container with a terminal has one raw stream.
		_, err = io.Copy(out, rc)
		out.flush()
	} else {
		// Otherwise Docker interleaves stdout and stderr in frames.
		errOut := &lineSplitter{container: name, stream: "stderr", emit: emit}
		_, err = stdcopy.StdCopy(out, errOut, rc)
		out.flush()
		errOut.flush()
	}
	if ctx.Err() != nil {
		return nil // the reader was told to stop; that is not a failure
	}
	return err
}

// lineSplitter is an io.Writer that turns a byte stream into LogLines.
type lineSplitter struct {
	container, stream string
	emit              func(LogLine)

	buf []byte
	// midLine is set after part of a long line was emitted: what follows, up
	// to the next newline, continues that line and has no timestamp of its own.
	midLine  bool
	lineTime string
}

func (w *lineSplitter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		if i := bytes.IndexByte(w.buf, '\n'); i >= 0 {
			w.line(w.buf[:i])
			w.buf = w.buf[i+1:]
			continue
		}
		if len(w.buf) < MaxLine+maxStamp {
			break
		}
		// No newline in sight: hand over a full piece and keep reading.
		if !w.midLine {
			ts, rest := splitTimestamp(string(w.buf[:maxStamp]))
			w.lineTime = ts
			w.buf = w.buf[maxStamp-len(rest):]
			w.midLine = true
		}
		w.send(string(w.buf[:MaxLine]), true)
		w.buf = w.buf[MaxLine:]
	}
	// Do not let the consumed prefix pin a large array.
	if len(w.buf) == 0 {
		w.buf = nil
	}
	return len(p), nil
}

// line handles one complete line (without its newline).
func (w *lineSplitter) line(raw []byte) {
	text := strings.TrimSuffix(string(raw), "\r")
	if w.midLine {
		// The pieces sent so far were marked as continuing, so the line is
		// always closed with a final piece, even an empty one.
		w.midLine = false
	} else {
		w.lineTime, text = splitTimestamp(text)
	}
	for len(text) > MaxLine {
		w.send(text[:MaxLine], true)
		text = text[MaxLine:]
	}
	w.send(text, false)
}

// flush emits whatever is left when the stream ends without a final newline.
func (w *lineSplitter) flush() {
	if len(w.buf) > 0 {
		w.line(w.buf)
		w.buf = nil
	}
}

func (w *lineSplitter) send(text string, partial bool) {
	w.emit(LogLine{
		Container: w.container,
		Stream:    w.stream,
		Time:      w.lineTime,
		// Logs can hold anything, including bytes that are not text.
		Text:    strings.ToValidUTF8(text, "�"),
		Partial: partial,
	})
}

// splitTimestamp separates the timestamp Docker puts in front of a log line
// from the line itself. A line that does not start with a timestamp is
// returned whole.
func splitTimestamp(raw string) (ts, text string) {
	stamp, rest, found := strings.Cut(raw, " ")
	if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
		return "", raw
	}
	if !found {
		return stamp, ""
	}
	return stamp, rest
}

// MergeOptions select what MergedLogs reads.
type MergeOptions struct {
	// Tail is how many of the newest lines, across all containers, to start
	// with: a number, or "all". Empty means "all".
	Tail string
	// Follow continues with new lines after the backlog.
	Follow bool
}

// MergedBacklogCap is the most lines read from each container for the merged
// view's backlog. The backlog is held in memory to be sorted by time, Docker
// keeps logs without limit by default, and the browser shows at most this
// many lines anyway.
const MergedBacklogCap = 10_000

// MergedLogs reads the logs of several containers as one. The backlog is
// sorted by time across the containers; when following, new lines are passed
// on as they arrive. emit is never called concurrently.
func MergedLogs(ctx context.Context, cli dockerapi.Client, containers []services.Container, o MergeOptions, emit func(LogLine)) error {
	perContainer := strconv.Itoa(MergedBacklogCap)
	if n, err := strconv.Atoi(o.Tail); err == nil && n >= 0 && n < MergedBacklogCap {
		perContainer = o.Tail
	}

	backlogs := make([][]LogLine, len(containers))
	errs := make([]error, len(containers))
	var wg sync.WaitGroup
	for i, c := range containers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = ReadLogs(ctx, cli, c.ID, c.Name, LogOptions{Tail: perContainer}, func(l LogLine) {
				backlogs[i] = append(backlogs[i], l)
			})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	var all []LogLine
	lastSeen := make([]string, len(containers))
	for i, lines := range backlogs {
		all = append(all, lines...)
		for j := len(lines) - 1; j >= 0; j-- {
			if lines[j].Time != "" {
				lastSeen[i] = lines[j].Time
				break
			}
		}
	}
	// Stable, so lines with equal times keep each container's own order.
	slices.SortStableFunc(all, func(a, b LogLine) int { return parseStamp(a.Time).Compare(parseStamp(b.Time)) })
	if n, err := strconv.Atoi(o.Tail); err == nil && n >= 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	for _, l := range all {
		emit(l)
	}
	if !o.Follow {
		return nil
	}

	var mu sync.Mutex
	for i, c := range containers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Docker includes the line at the "since" instant again; skip
			// until something newer than the backlog shows up.
			boundary, skipping := parseStamp(lastSeen[i]), lastSeen[i] != ""
			errs[i] = ReadLogs(ctx, cli, c.ID, c.Name, LogOptions{Tail: "all", Follow: true, Since: lastSeen[i]}, func(l LogLine) {
				if skipping {
					if l.Time != "" && !parseStamp(l.Time).After(boundary) {
						return
					}
					skipping = false
				}
				mu.Lock()
				defer mu.Unlock()
				emit(l)
			})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func parseStamp(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
