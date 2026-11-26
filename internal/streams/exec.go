package streams

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/moby/moby/client"

	"dockpanel/internal/dockerapi"
)

// ShellCommand is what a shell session runs: bash when the container has it,
// otherwise sh.
var ShellCommand = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}

var (
	// ErrNoShell means the container image has no shell to run, as with
	// "distroless" and "scratch" images.
	ErrNoShell = errors.New("this container has no shell")
	// ErrNotRunning means the container is stopped; a shell needs a running one.
	ErrNotRunning = errors.New("the container is not running")
)

// ExecSession is an interactive shell inside a container. Read returns what
// the shell prints; Write sends what the person types.
type ExecSession struct {
	cli       dockerapi.Client
	id        string
	attached  client.ExecAttachResult
	closeOnce sync.Once
}

// StartShell opens a shell with a terminal of the given size in a running
// container. A zero size means 80 by 24.
func StartShell(ctx context.Context, cli dockerapi.Client, containerID string, cols, rows uint) (*ExecSession, error) {
	inspected, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	if st := inspected.Container.State; st == nil || !st.Running || st.Restarting {
		return nil, ErrNotRunning
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	size := client.ConsoleSize{Height: rows, Width: cols}

	created, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		TTY:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ConsoleSize:  size,
		Env:          []string{"TERM=xterm-256color"},
		Cmd:          ShellCommand,
	})
	if err != nil {
		return nil, fmt.Errorf("open a shell: %w", err)
	}
	attached, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true, ConsoleSize: size})
	if err != nil {
		if missingExecutable(err) {
			return nil, ErrNoShell
		}
		return nil, fmt.Errorf("open a shell: %w", err)
	}
	return &ExecSession{cli: cli, id: created.ID, attached: attached}, nil
}

func (s *ExecSession) Read(p []byte) (int, error)  { return s.attached.Reader.Read(p) }
func (s *ExecSession) Write(p []byte) (int, error) { return s.attached.Conn.Write(p) }

// Resize tells the shell its terminal changed size. A zero size, which a
// hidden terminal reports, is ignored.
func (s *ExecSession) Resize(ctx context.Context, cols, rows uint) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	_, err := s.cli.ExecResize(ctx, s.id, client.ExecResizeOptions{Height: rows, Width: cols})
	return err
}

// ExitCode reports how the shell ended. Codes 126 and 127 mean the shell
// could not be run at all, which is returned as ErrNoShell.
func (s *ExecSession) ExitCode(ctx context.Context) (int, error) {
	inspected, err := s.cli.ExecInspect(ctx, s.id, client.ExecInspectOptions{})
	if err != nil {
		return -1, err
	}
	if inspected.ExitCode == 126 || inspected.ExitCode == 127 {
		return inspected.ExitCode, ErrNoShell
	}
	return inspected.ExitCode, nil
}

// Close ends the session. The shell sees its terminal hang up and exits.
func (s *ExecSession) Close() error {
	s.closeOnce.Do(s.attached.Close)
	return nil
}

func missingExecutable(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "executable file not found")
}
