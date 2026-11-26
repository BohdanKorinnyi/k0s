package hosts

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	accepted := []string{
		"unix:///var/run/docker.sock",
		"tcp://10.0.0.5:2376",
		"tcp://docker.internal",
		"tcp://[fd00::5]:2376",
		"ssh://deploy@aws.example.com",
		"ssh://aws.example.com",
		"ssh://deploy@host:2222/run/user/1000/docker.sock",
	}
	for _, raw := range accepted {
		if err := ValidateURL(raw); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want accepted", raw, err)
		}
	}

	rejected := []struct{ raw, contains string }{
		{"", "empty"},
		{"http://x", "unsupported scheme"},
		{"npipe:////./pipe/docker_engine", "unsupported scheme"},
		{"aws.example.com", "unsupported scheme"},
		{"tcp://", "host"},
		{"tcp://:2376", "host"},
		{"ssh://", "host"},
		{"unix://", "path"},
		{"ssh://-oProxyCommand=x@host", "must not start with"},
		{"ssh://user@-oProxyCommand=x", "must not start with"},
		{"ssh://user:secret@host", "password"},
		{"ssh://user@host/tmp/x;rm -rf $HOME", "socket path"},
		{"ssh://user@host/run/docker.sock?x=1", "query"},
		{"tcp://host:2376/v1", "path"},
		{"tcp://host:notaport", ""},
	}
	for _, c := range rejected {
		err := ValidateURL(c.raw)
		if err == nil {
			t.Errorf("ValidateURL(%q) accepted, want an error", c.raw)
			continue
		}
		if c.contains != "" && !strings.Contains(err.Error(), c.contains) {
			t.Errorf("ValidateURL(%q) = %q, want it to mention %q", c.raw, err, c.contains)
		}
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSSHArgs(t *testing.T) {
	o := Options{KnownHostsFile: "/data/known_hosts"}
	got := sshArgs(mustParse(t, "ssh://deploy@aws.example.com:2222"), o)
	want := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=/data/known_hosts ~/.ssh/known_hosts",
		"-o", "IgnoreUnknown=UseKeychain",
		"-l", "deploy",
		"-p", "2222",
		"--", "aws.example.com",
		"docker", "system", "dial-stdio",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sshArgs =\n  %q\nwant\n  %q", got, want)
	}
}

func TestSSHArgsVariants(t *testing.T) {
	tail := func(args []string) []string {
		for i, a := range args {
			if a == "--" {
				return args[i:]
			}
		}
		return nil
	}
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}

	// A path in the URL selects the Docker socket on the far side.
	withPath := sshArgs(mustParse(t, "ssh://deploy@host/run/user/1000/docker.sock"), Options{})
	wantTail := []string{"--", "host", "docker", "-H", "unix:///run/user/1000/docker.sock", "system", "dial-stdio"}
	if got := tail(withPath); !reflect.DeepEqual(got, wantTail) {
		t.Errorf("tail with a socket path = %q, want %q", got, wantTail)
	}

	// No user and no port: neither flag appears.
	bare := sshArgs(mustParse(t, "ssh://host"), Options{})
	if has(bare, "-l") || has(bare, "-p") {
		t.Errorf("bare host produced -l or -p: %q", bare)
	}

	// Without a known-hosts file of our own, ssh's default files are used.
	for _, a := range bare {
		if strings.HasPrefix(a, "UserKnownHostsFile=") {
			t.Errorf("UserKnownHostsFile set although none was configured: %q", a)
		}
	}
}

func TestTCPAddr(t *testing.T) {
	cases := []struct {
		raw  string
		tls  bool
		want string
	}{
		{"tcp://10.0.0.5:2376", true, "10.0.0.5:2376"},
		{"tcp://10.0.0.5", false, "10.0.0.5:2375"},
		{"tcp://10.0.0.5", true, "10.0.0.5:2376"},
		{"tcp://[fd00::5]", true, "[fd00::5]:2376"},
	}
	for _, c := range cases {
		if got := tcpAddr(mustParse(t, c.raw), c.tls); got != c.want {
			t.Errorf("tcpAddr(%q, tls=%v) = %q, want %q", c.raw, c.tls, got, c.want)
		}
	}
}
