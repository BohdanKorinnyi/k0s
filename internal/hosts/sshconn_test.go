package hosts

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandConnEcho(t *testing.T) {
	conn, err := newCommandConn(context.Background(), "cat")
	if err != nil {
		t.Fatalf("newCommandConn: %v", err)
	}
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("read %q, %v; want the echoed line", line, err)
	}

	done := make(chan error, 1)
	go func() { done <- conn.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return: the process was not ended")
	}
	if _, err := conn.Write([]byte("x")); err == nil {
		t.Error("Write after Close succeeded")
	}
}

func TestCommandConnStderr(t *testing.T) {
	conn, err := newCommandConn(context.Background(), "sh", "-c", "echo boom >&2; exit 3")
	if err != nil {
		t.Fatalf("newCommandConn: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, 16)
	_, err = conn.Read(buf)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Read err = %v, want one carrying the command's stderr", err)
	}
}

func TestCommandConnMissingBinary(t *testing.T) {
	_, err := newCommandConn(context.Background(), "definitely-not-a-real-binary")
	if err == nil {
		t.Fatal("want an error when the command does not exist")
	}
}

func TestCommandConnCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newCommandConn(ctx, "cat"); err == nil {
		t.Fatal("want an error when the context is already cancelled")
	}
}

// The connection outlives the context it was dialled with: HTTP keeps
// connections for later requests, long after the first request's context ends.
func TestCommandConnOutlivesDialContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := newCommandConn(ctx, "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	time.Sleep(50 * time.Millisecond)
	if _, err := conn.Write([]byte("still here\n")); err != nil {
		t.Fatalf("Write after the dial context ended: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "still here\n" {
		t.Errorf("read %q, %v", line, err)
	}
}
