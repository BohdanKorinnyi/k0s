package hosts

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// commandConn is a network connection whose two directions are the stdin and
// stdout of a running command. With `ssh host docker system dial-stdio` as the
// command, it is a connection to the remote Docker daemon.
type commandConn struct {
	name   string
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	stderr tailBuffer

	done      chan struct{} // closed when the command has exited
	waitErr   error
	closeOnce sync.Once
}

// newCommandConn starts the command and returns the connection.
//
// ctx is honoured only up to the start: HTTP keeps connections for later
// requests, so the connection must outlive the request that dialled it.
func newCommandConn(ctx context.Context, name string, args ...string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return nil, err
	}

	c := &commandConn{
		name:   filepath.Base(name),
		cmd:    exec.Command(name, args...),
		stdin:  stdinW,
		stdout: stdoutR,
		done:   make(chan struct{}),
	}
	c.cmd.Stdin = stdinR
	c.cmd.Stdout = stdoutW
	c.cmd.Stderr = &c.stderr
	// Do not wait forever for stderr if the command left a child holding it.
	c.cmd.WaitDelay = time.Second

	err = c.cmd.Start()
	// The child has its own copies of these ends now.
	stdinR.Close()
	stdoutW.Close()
	if err != nil {
		stdinW.Close()
		stdoutR.Close()
		return nil, fmt.Errorf("start %s: %w", c.name, err)
	}

	go func() {
		c.waitErr = c.cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

func (c *commandConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if err != nil {
		err = c.explain(err)
	}
	return n, err
}

func (c *commandConn) Write(p []byte) (int, error) {
	n, err := c.stdin.Write(p)
	if err != nil {
		err = c.explain(err)
	}
	return n, err
}

// explain replaces a bare "EOF" or "broken pipe" with what the command said
// when it failed, e.g. "ssh: deploy@host: Permission denied (publickey)."
func (c *commandConn) explain(err error) error {
	select {
	case <-c.done:
	case <-time.After(500 * time.Millisecond):
		return err
	}
	if c.waitErr == nil {
		return err
	}
	if msg := strings.TrimSpace(c.stderr.String()); msg != "" {
		return fmt.Errorf("%s: %s", c.name, msg)
	}
	return fmt.Errorf("%s: %w", c.name, c.waitErr)
}

// Close ends the command. Closing its stdin is the polite way; if it has not
// exited a moment later it is killed.
func (c *commandConn) Close() error {
	c.closeOnce.Do(func() {
		c.stdin.Close()
		select {
		case <-c.done:
		case <-time.After(time.Second):
			_ = c.cmd.Process.Kill()
		}
		c.stdout.Close()
	})
	return nil
}

// CloseWrite half-closes the connection: the far side sees end of input.
func (c *commandConn) CloseWrite() error { return c.stdin.Close() }

func (c *commandConn) LocalAddr() net.Addr  { return commandAddr(c.name) }
func (c *commandConn) RemoteAddr() net.Addr { return commandAddr(c.name) }

// Deadlines are not supported on pipes to a child process. Callers bound their
// work with contexts instead; reporting success keeps net/http content.
func (c *commandConn) SetDeadline(time.Time) error      { return nil }
func (c *commandConn) SetReadDeadline(time.Time) error  { return nil }
func (c *commandConn) SetWriteDeadline(time.Time) error { return nil }

type commandAddr string

func (a commandAddr) Network() string { return "command" }
func (a commandAddr) String() string  { return string(a) }

// tailBuffer keeps the last few kilobytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

const tailBufferSize = 4096

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > tailBufferSize {
		b.buf = b.buf[len(b.buf)-tailBufferSize:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

var _ net.Conn = (*commandConn)(nil)

// errNoSSH explains a missing ssh client in terms of what to do about it.
var errNoSSH = errors.New("no ssh client found; ssh:// hosts need the `ssh` command to be installed")
