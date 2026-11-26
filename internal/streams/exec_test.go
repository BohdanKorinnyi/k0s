package streams

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	cerrdefs "github.com/containerd/errdefs"

	"dc0s/internal/dockerapi/dockertest"
)

func TestStartShell(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))

	s, err := StartShell(ctx, f, "api-1", 120, 40)
	if err != nil {
		t.Fatalf("StartShell: %v", err)
	}
	defer s.Close()

	o := f.LastExec()
	if !reflect.DeepEqual(o.Cmd, ShellCommand) {
		t.Errorf("Cmd = %q, want %q", o.Cmd, ShellCommand)
	}
	if !o.TTY || !o.AttachStdin || !o.AttachStdout || !o.AttachStderr {
		t.Errorf("exec options = %+v, want a terminal with all three streams attached", o)
	}
	if !slices.Contains(o.Env, "TERM=xterm-256color") {
		t.Errorf("Env = %q, want TERM=xterm-256color", o.Env)
	}
	if o.ConsoleSize.Width != 120 || o.ConsoleSize.Height != 40 {
		t.Errorf("ConsoleSize = %+v, want 120x40", o.ConsoleSize)
	}

	// The fake echoes: what is typed comes back.
	if _, err := s.Write([]byte("echo hi\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 8)
	n, err := s.Read(buf)
	if err != nil || string(buf[:n]) != "echo hi\n" {
		t.Errorf("Read = %q, %v", buf[:n], err)
	}
}

func TestStartShellDefaultSize(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	s, err := StartShell(ctx, f, "api-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if size := f.LastExec().ConsoleSize; size.Width != 80 || size.Height != 24 {
		t.Errorf("ConsoleSize = %+v, want 80x24 when the browser gave none", size)
	}
}

func TestStartShellNotRunning(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Stopped("job", "app:1", 0))
	if _, err := StartShell(ctx, f, "job", 80, 24); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
	if calls := f.CallLog(); len(calls) != 0 {
		t.Errorf("calls = %q, want none for a stopped container", calls)
	}
}

func TestStartShellUnknownContainer(t *testing.T) {
	if _, err := StartShell(ctx, dockertest.New(), "nope", 80, 24); !cerrdefs.IsNotFound(err) {
		t.Errorf("err = %v, want not found", err)
	}
}

func TestStartShellNoShellAtStart(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("distroless", "app:1"))
	f.Fail("ExecAttach", errors.New(`OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/sh": stat /bin/sh: no such file or directory`))
	if _, err := StartShell(ctx, f, "distroless", 80, 24); !errors.Is(err, ErrNoShell) {
		t.Errorf("err = %v, want ErrNoShell", err)
	}
}

func TestExitCode(t *testing.T) {
	for _, c := range []struct {
		code    int
		noShell bool
	}{{0, false}, {130, false}, {126, true}, {127, true}} {
		f := dockertest.New()
		f.AddContainer(dockertest.Running("api-1", "app:1"))
		f.ExecExitCode = c.code
		s, err := StartShell(ctx, f, "api-1", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		code, err := s.ExitCode(ctx)
		if code != c.code {
			t.Errorf("ExitCode = %d, want %d", code, c.code)
		}
		if errors.Is(err, ErrNoShell) != c.noShell {
			t.Errorf("exit %d: err = %v, want ErrNoShell: %v", c.code, err, c.noShell)
		}
		s.Close()
	}
}

func TestResize(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	s, err := StartShell(ctx, f, "api-1", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Resize(ctx, 132, 50); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if !slices.Contains(f.CallLog(), "ExecResize 132x50") {
		t.Errorf("calls = %q, want ExecResize 132x50", f.CallLog())
	}
	// A zero size (a hidden terminal) is ignored rather than sent.
	f.ResetCalls()
	if err := s.Resize(ctx, 0, 0); err != nil {
		t.Errorf("Resize(0,0): %v", err)
	}
	if len(f.CallLog()) != 0 {
		t.Errorf("calls = %q, want none for a zero size", f.CallLog())
	}
}

func TestCloseEndsTheSession(t *testing.T) {
	f := dockertest.New()
	f.AddContainer(dockertest.Running("api-1", "app:1"))
	s, err := StartShell(ctx, f, "api-1", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := s.Write([]byte("x")); err == nil {
		t.Error("Write after Close succeeded")
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
