# dockpanel First Version Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A web control panel, shipped as one Docker image, that lists the services on several Docker hosts and lets the user restart them, pull-and-restart them, read logs, view env vars and open a shell.

**Architecture:** One Go program serves a JSON/WebSocket API and the built React frontend. It talks straight to each Docker daemon over the host's connection string (socket, TCP+TLS, SSH); nothing is installed on hosts. State is one JSON file in a data volume.

**Tech Stack:** Go 1.27 (stdlib `net/http` routing), `github.com/moby/moby/client` v0.6.3 + `github.com/moby/moby/api` v1.57.0, `github.com/coder/websocket` v1.8.15, `github.com/distribution/reference` v0.6.0. Frontend: Node 22, React 19.3, react-router 8.5, Vite 8.3, TypeScript 7.0, Vitest 5.1, `@xterm/xterm` 6.0 + `@xterm/addon-fit` 0.11, `anser` 2.3.

**Spec:** `docs/superpowers/specs/2026-11-26-dockpanel-design.md`

## Global Constraints

- The product name lives in `internal/appinfo` (`Name = "dockpanel"`, `EnvPrefix = "DOCKPANEL_"`) and `web/src/appinfo.ts`. Nothing else hardcodes it except the Go module path (`module dockpanel`), the image name in `Makefile`/`compose.yaml`, and the docs.
- Licence MIT, holder "dockpanel contributors".
- The only prerequisite to build and test is Docker. Every Go command goes through `./scripts/go` (uses local Go when present, otherwise a `golang:1.27-alpine` container with cache volumes `dockpanel-gomod`, `dockpanel-gobuild`).
- Secrets (registry passwords, TLS client keys) are never present in any API response. The data file is mode `0600`, its directory `0700`.
- Default allowed hostnames: `localhost`, `127.0.0.1`, `::1`. Documented run command publishes on `127.0.0.1` only.
- Untrusted text (log lines, env values, container names, labels, Docker error text) is rendered as text nodes only. No `innerHTML` / `dangerouslySetInnerHTML` anywhere in `web/src`.
- Library APIs newer than the author's memory (moby client v0.6, react-router 8, TypeScript 7, Vite 8): read the installed package's docs (`./scripts/go doc …`, `node_modules/<pkg>/README.md`) before using a call; do not write from memory.
- Fixed values: host call timeout 15s, ping 5s, action time limit 15m, health wait 2m, start grace 3s, poll 500ms, UI refresh 5s, update-check age 15m, log tail default 500 (choices 100/500/2000/all), browser log buffer 10 000 lines, session lifetime 7 days, 10 failed logins per minute.
- Work on branch `first-version`. Commit at the end of every task; never push.

## Review Focus

1. **Host unreachable or slow** (SSH down, firewall): the list must show the error with a retry within the timeout, never hang or show an empty table. → Tasks 4, 10, 13.
2. **A container disappears between listing and inspecting** (it was replaced or removed mid-refresh): the list must skip it, not fail. → Task 3.
3. **Hostile or huge log content** (a 600 KB line, binary bytes, colour codes, `<script>` text): must not error, freeze or inject markup. → Tasks 8, 12, 14.
4. **Image references in every shape** (`nginx`, `app:develop`, `localhost:5000/app`, `repo@sha256:…`, a bare image ID): registry matching, update check and pull must classify each correctly. → Task 5.
5. **Two actions on one service at once, or the browser closing mid-action**: the second is refused, and a started action runs to completion. → Task 10.

Also pinned by tests in their owning tasks: a hand-edited or corrupt data file must stop startup with a clear error and leave the file untouched (Task 2); an SSH host or user beginning with `-` must be rejected (Task 4); env entries containing `=` or no `=` (Task 10).

## How every task runs

Each task below lists **Files**, **Interfaces**, **Tests** (name → what it asserts, with exact values) and **Verify**. The steps are the same every time:

- [ ] Write the listed tests; run them and see them fail for the right reason.
- [ ] Implement until they pass.
- [ ] Run the task's Verify command and the whole suite (`make test`).
- [ ] Commit.

---

### Task 1: Skeleton, tooling, configuration

**Files:** `go.mod`, `scripts/go`, `Makefile`, `LICENSE`, `.dockerignore`, `internal/appinfo/appinfo.go`, `internal/config/config.go`, `internal/config/config_test.go`, `cmd/dockpanel/main.go` (prints name and version only; completed in Task 11).

**Interfaces — produces:**

```go
// appinfo
const Name = "dockpanel"
const EnvPrefix = "DOCKPANEL_"
var Version = "dev" // set with -ldflags

// config
type Config struct {
    Addr          string        // ADDR, default ":8080"
    DataDir       string        // DATA_DIR, default "/data"
    Password      string        // PASSWORD, or trimmed contents of PASSWORD_FILE
    AllowedHosts  []string      // defaults + ALLOWED_HOSTS (comma separated)
    HealthTimeout time.Duration // HEALTH_TIMEOUT, default 2m
    LocalSocket   string        // LOCAL_SOCKET, default "/var/run/docker.sock"
}
func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error)
```

`Makefile` targets: `test` (Go + web), `test-go`, `test-web`, `test-integration`, `web`, `build` (image `dockpanel:dev`), `run`.

**Tests:**
- `TestLoadDefaults` → `Addr == ":8080"`, `DataDir == "/data"`, `AllowedHosts == ["localhost","127.0.0.1","::1"]`, `HealthTimeout == 2*time.Minute`, `LocalSocket == "/var/run/docker.sock"`, `Password == ""`.
- `TestLoadAllowedHosts` → `ALLOWED_HOSTS=" panel.lan, Box.Local "` appends `"panel.lan","box.local"` after the defaults.
- `TestLoadPasswordFile` → file content `"s3cret\n"` gives `Password == "s3cret"`; `PASSWORD` and `PASSWORD_FILE` both set → error naming both variables.
- `TestLoadBadDuration` → `HEALTH_TIMEOUT=soon` → error containing `DOCKPANEL_HEALTH_TIMEOUT`.

**Verify:** `./scripts/go test ./internal/config/...` → `ok`; `./scripts/go run ./cmd/dockpanel -version` → `dockpanel dev`.

---

### Task 2: Data file store

**Files:** `internal/store/store.go`, `internal/store/types.go`, `internal/store/store_test.go`.

**Interfaces — produces:**

```go
type Host struct {
    ID        string `json:"id"`   // slug of the name; used in URLs; never changes
    Name      string `json:"name"`
    URL       string `json:"url"`
    TLS       *TLS   `json:"tls,omitempty"`
    Protected bool   `json:"protected"`
}
type TLS struct{ CA, Cert, Key string } // PEM text; json: ca, cert, key
type Registry struct{ ID, Address, Username, Password string } // json: id, address, username, password
var ErrNotFound, ErrDuplicate error

func Open(path string) (*Store, error)
func (s *Store) IsNew() bool                          // true when the file did not exist at Open
func (s *Store) Hosts() []Host
func (s *Store) Host(id string) (Host, bool)
func (s *Store) SaveHost(h Host) (Host, error)        // h.ID == "" creates
func (s *Store) DeleteHost(id string) error
func (s *Store) Registries() []Registry
func (s *Store) SaveRegistry(r Registry) (Registry, error)
func (s *Store) DeleteRegistry(id string) error
```

Writes are atomic (temp file in the same directory, then rename). Registry IDs are 8 random hex characters.

**Tests:**
- `TestHostIDs` → name `"AWS Prod"` → ID `"aws-prod"`; name `"!!!"` → `"host"`; names `"a b"` then `"a-b"` → IDs `"a-b"`, `"a-b-2"`.
- `TestDuplicateHostName` → `"aws"` then `"AWS"` → `ErrDuplicate`. Empty name or empty URL → error.
- `TestUpdateKeepsID` → renaming host `aws-prod` to `"Production"` keeps ID `"aws-prod"`.
- `TestWriteOnlySecrets` → update with `TLS.Key == ""` and non-nil `TLS` keeps the stored key; update with `TLS == nil` removes TLS; registry update with `Password == ""` keeps the stored password.
- `TestPermissionsAndReload` → file mode `0600`, directory `0700`; reopening returns the same hosts and registries; `IsNew()` is true first time, false after.
- `TestCorruptFile` → file containing `{not json` → `Open` returns an error containing the path, and the file's bytes are unchanged.
- `TestNotFound` → `DeleteHost("nope")`, `DeleteRegistry("nope")`, updating an unknown ID → `ErrNotFound`.
- `TestConcurrentSaves` (run with `-race`) → 20 parallel `SaveHost` calls with distinct names leave 20 hosts.

**Verify:** `./scripts/go test -race ./internal/store/...` → `ok`.

---

### Task 3: Docker interface, test fake, service grouping

**Files:** `internal/dockerapi/dockerapi.go`, `internal/dockerapi/dockertest/fake.go`, `internal/dockerapi/dockertest/fake_test.go`, `internal/services/services.go`, `internal/services/services_test.go`.

**Interfaces — produces:**

`dockerapi.Client` is the subset of `*client.Client` the project uses, with identical signatures so the real client satisfies it (`var _ Client = (*client.Client)(nil)`): `Ping`, `ServerVersion`, `ContainerList`, `ContainerInspect`, `ContainerStart`, `ContainerStop`, `ContainerRestart`, `ContainerRemove`, `ContainerRename`, `ContainerCreate`, `ContainerLogs`, `ImagePull`, `ImageInspect`, `DistributionInspect`, `ExecCreate`, `ExecAttach`, `ExecResize`, `ExecInspect`, `NetworkConnect`, `Close`.

`dockertest.Fake` implements it in memory: `New()`, `AddContainer(container.InspectResponse)`, `AddImage(ref string, image.InspectResponse)`, `SetRemoteDigest(ref, digest string)`, `SetPullResult(ref string, image.InspectResponse)`, `Fail(method string, err error)`, `FailFor(method, containerName string, err error)`, field `Calls []string`. Every state-changing call is appended to `Calls` as `"<Method> <container name>"` (rename as `"ContainerRename <old>-><new>"`). Start/stop/restart/remove/rename/create change the stored containers the way Docker would; inspecting an unknown container returns an error for which `cerrdefs.IsNotFound` is true.

```go
// services
type Container struct {
    ID, Name, Image, ImageID string
    State     string    // Docker's: created running paused restarting removing exited dead
    Health    string    // "" healthy unhealthy starting
    ExitCode  int
    StartedAt time.Time
    Restarts  int
}
type Service struct {
    Key        string   // "c~<project>~<service>" for Compose, "s~<container name>" otherwise
    Name       string
    Project    string   // "" when not from Compose
    Image      string   // "" when containers differ
    Images     []string // distinct references, sorted
    Running    int
    Total      int
    State      string   // restarting | unhealthy | running | partial | stopped
    ExitCode   int      // of the most recently finished container when State == "stopped"
    StartedAt  time.Time // latest start among running containers; zero when none
    Restarts   int      // sum
    Containers []Container
}
type Group struct{ Project string; Services []Service }
var ErrNotFound error

func List(ctx context.Context, cli dockerapi.Client) ([]Group, error)
func Find(groups []Group, key string) (Service, bool)
func Get(ctx context.Context, cli dockerapi.Client, key string) (Service, error)
```

`List` lists all containers (running or not), inspects each (at most 8 at once) and builds everything from the inspect data. Compose labels: `com.docker.compose.project`, `.service`, `.container-number`, `.oneoff`.

**Tests:**
- `TestComposeGrouping` → two containers, project `shop`, service `api`, numbers 2 and 1 → one service: `Key "c~shop~api"`, `Name "api"`, `Running 2`, `Total 2`, `State "running"`, containers ordered `shop-api-1`, `shop-api-2`.
- `TestStandalone` → unlabelled `/redis-cache` → last group with `Project ""`, service `Key "s~redis-cache"`, `Name "redis-cache"`.
- `TestOneOff` → label `com.docker.compose.oneoff=True` → its own row in the project's group with key `s~<name>`.
- `TestOrdering` → groups by project ascending with `""` last; services by name.
- `TestState` (table) → any restarting → `restarting`; else any health `unhealthy` → `unhealthy`; all running → `running`; one of two running → `partial`; none running → `stopped` with the latest finisher's exit code (137).
- `TestMixedImages` → images `app:1`, `app:2` → `Image ""`, `Images ["app:1","app:2"]`.
- `TestTotals` → `Restarts` is the sum (3+11 → 14); `StartedAt` is the latest among running.
- `TestVanishedContainer` → inspect of one listed container returns not-found → that container is skipped, `List` returns no error.
- `TestListError` → `ContainerList` fails → error returned. `TestGetNotFound` → `ErrNotFound`.

**Verify:** `./scripts/go test ./internal/dockerapi/... ./internal/services/...` → `ok`.

---

### Task 4: Host connections

**Files:** `internal/hosts/url.go`, `internal/hosts/sshconn.go`, `internal/hosts/dial.go`, `internal/hosts/manager.go`, tests beside each.

**Interfaces — consumes:** `store.Host`, `store.Store`, `dockerapi.Client`. **Produces:**

```go
type Options struct{ KnownHostsFile, ControlDir string }
func ValidateURL(raw string) error
func Dial(h store.Host, o Options) (*client.Client, error) // builds, does not connect
func NewManager(st *store.Store, o Options) *Manager
func (m *Manager) Client(ctx context.Context, hostID string) (dockerapi.Client, error) // cached per host
func (m *Manager) Invalidate(hostID string)
func (m *Manager) Test(ctx context.Context, h store.Host) (version string, err error) // 5s limit
func (m *Manager) Close() error
// unexported, tested:
func sshArgs(u *url.URL, o Options) []string
func newCommandConn(ctx context.Context, name string, args ...string) (net.Conn, error)
```

SSH hosts run the system `ssh` client with `docker system dial-stdio` on the far side and speak the Docker API over its stdin/stdout. A path in the URL selects the remote socket. TLS for `tcp://` is built in memory from the stored PEM text.

**Tests:**
- `TestValidateURL` (table) → accepted: `unix:///var/run/docker.sock`, `tcp://10.0.0.5:2376`, `ssh://deploy@aws.example.com`, `ssh://deploy@host:2222/run/user/1000/docker.sock`. Rejected: `""`, `http://x`, `npipe:////./pipe/docker` (message contains `unsupported scheme`), `tcp://`, `ssh://`, `ssh://-oProxyCommand=x@host`, `ssh://user@-oProxyCommand=x`.
- `TestSSHArgs` → for `ssh://deploy@aws.example.com:2222` with `KnownHostsFile "/data/known_hosts"`, `ControlDir "/tmp/dp"` the result is exactly:
  `-T -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -o "UserKnownHostsFile=/data/known_hosts ~/.ssh/known_hosts" -o IgnoreUnknown=UseKeychain -o ControlMaster=auto -o ControlPath=/tmp/dp/%C -o ControlPersist=120 -l deploy -p 2222 -- aws.example.com docker system dial-stdio`
  (each `-o` value one argument). With path `/run/user/1000/docker.sock` the tail is `docker -H unix:///run/user/1000/docker.sock system dial-stdio`. No user → no `-l`; no port → no `-p`.
- `TestCommandConnEcho` → `newCommandConn(ctx, "cat")`: write `"ping\n"`, read back `"ping\n"`; `Close` ends the process.
- `TestCommandConnStderr` → `sh -c 'echo boom >&2; exit 3'` → `Read` returns an error containing `boom`.
- `TestDialBadTLS` → TLS with `Cert: "not pem"` → error containing `certificate`.
- `TestManagerCaches` → two `Client` calls return the same value; after `Invalidate` a different one; unknown ID → `store.ErrNotFound`.
- `TestTestUnreachable` → host `tcp://127.0.0.1:1` → `Test` returns an error in under 6 seconds.

**Verify:** `./scripts/go test ./internal/hosts/...` → `ok`.

---

### Task 5: Registry logins and update check

**Files:** `internal/registryauth/registryauth.go` (+ test), `internal/updates/updates.go` (+ test).

**Interfaces — produces:**

```go
// registryauth
type Credential struct{ Address, Username, Password string }
type Provider interface {
    Login(ctx context.Context, registryHost string) (Credential, bool, error)
}
type Static struct{ List func() []Credential } // implements Provider
func Normalize(address string) string
func RegistryHost(imageRef string) (string, error)
func Pullable(imageRef string) bool
func Encoded(ctx context.Context, p Provider, imageRef string) (string, error) // "" when none matches

// updates
type Status string // "up_to_date" "update_available" "unknown" "pinned"
type Result struct{ Status Status; Reason, LocalImageID string; CheckedAt time.Time }
const MaxAge = 15 * time.Minute
func NewChecker(auth registryauth.Provider, now func() time.Time) *Checker
func (c *Checker) Check(ctx context.Context, cli dockerapi.Client, hostID string, refs []string, force bool) map[string]Result
func (c *Checker) Cached(hostID, ref string) (Result, bool)
func (c *Checker) LastRun(hostID string) time.Time
func (c *Checker) Forget(hostID string)
func ForService(results []Result, containerImageIDs []string) (Status, string)
```

`Provider` is the extension seam for other login types (spec section 9). A check compares the digests in the local image's `RepoDigests` with `DistributionInspect(ref).Descriptor.Digest`, asked through the host's daemon.

**Tests:**
- `TestNormalize` → `"https://index.docker.io/v1/"`, `"registry-1.docker.io"`, `"docker.io"` → `"docker.io"`; `"GHCR.io/"` → `"ghcr.io"`; `"localhost:5000"` unchanged.
- `TestRegistryHost` → `nginx` → `docker.io`; `ghcr.io/acme/api:develop` → `ghcr.io`; `localhost:5000/app` → `localhost:5000`; `123456789.dkr.ecr.eu-west-1.amazonaws.com/app:1` → that host.
- `TestPullable` → true: `app`, `app:develop`, `localhost:5000/app`; false: `app@sha256:<64 hex>`, `app:1@sha256:<64 hex>`, `sha256:<64 hex>`, `<64 hex>`.
- `TestEncoded` → decoding the base64url result yields JSON with `username`, `password`, `serveraddress`; no matching credential → `""`.
- `TestCheckStatuses` → same digest → `up_to_date`; different → `update_available`; no `RepoDigests` → `unknown`, reason `built locally`; registry answers unauthorized → `unknown`, reason `needs a registry login`; other error → `unknown`, reason starting `registry error`; digest reference → `pinned` and the fake sees no calls.
- `TestCheckCache` → second `Check` inside 15 minutes makes no registry call; `force` does; after 15 minutes it does; `LastRun` returns the injected time.
- `TestCheckUsesLogin` → with a credential for the image's registry the fake receives a non-empty encoded login.
- `TestForService` → all `up_to_date` but one container's image ID differs from `LocalImageID` → `update_available`; any `update_available` → that; else any `unknown` → `unknown` with its reason; all `pinned` → `pinned`.

**Verify:** `./scripts/go test ./internal/registryauth/... ./internal/updates/...` → `ok`.

---

### Task 6: Actions — list and lifecycle

**Files:** `internal/actions/actions.go`, `internal/actions/lifecycle.go`, tests beside each.

**Interfaces — produces:**

```go
type Event struct {
    Step  string `json:"step"`
    Level string `json:"level"` // info success warning error
    Done  bool   `json:"done"`
}
type Progress func(Event)
type Target struct{ Service services.Service; ContainerID string } // ContainerID "" = whole service
type Env struct {
    Docker        dockerapi.Client
    Auth          registryauth.Provider
    HealthTimeout time.Duration // 2m
    StartGrace    time.Duration // 3s
    Poll          time.Duration // 500ms
}
type Info struct {
    ID             string `json:"id"`
    Label          string `json:"label"`
    Destructive    bool   `json:"destructive"`
    ContainerLevel bool   `json:"containerLevel"`
}
type Action interface {
    Info() Info
    Applies(svc services.Service) bool
    Run(ctx context.Context, env Env, t Target, report Progress) error
}
func NewRegistry() *Registry
func (r *Registry) Register(a Action) // panics on a duplicate ID
func (r *Registry) Get(id string) (Action, bool)
func (r *Registry) List() []Info
func (r *Registry) Applicable(svc services.Service) []string
func Default() *Registry
```

`Run` reports steps; the caller sends the final `Done` event from the returned error. This is the extension seam for actions (spec section 9): one file per action, registered in `Default`.

**Tests:**
- `TestDefaultActions` → after Task 7 `Default().List()` is exactly, in order: `{restart, "Restart", false, true}`, `{start, "Start", false, true}`, `{stop, "Stop", true, true}`, `{pull-restart, "Pull & restart", true, false}` (this task asserts the first three).
- `TestApplies` → restart: `Total > 0`; start: `Running < Total`; stop: `Running > 0`.
- `TestRestartService` → fake `Calls == ["ContainerRestart shop-api-1","ContainerRestart shop-api-2"]`, one `info` event per container naming it.
- `TestRestartOneContainer` → with `ContainerID` of `shop-api-2` only that call; an ID not in the service → error.
- `TestStopsAtFirstFailure` → first restart fails → error names `shop-api-1`; `shop-api-2` untouched.
- `TestStartSkipsRunning`, `TestStopSkipsStopped`.
- `TestRegisterDuplicatePanics`.

**Verify:** `./scripts/go test ./internal/actions/...` → `ok`.

---

### Task 7: Faithful replacement, pull and restart

**Files:** `internal/actions/recreate.go`, `internal/actions/pullrestart.go`, tests beside each; register in `Default`.

**Interfaces — consumes:** Task 6 types. **Produces:**

```go
func Replace(ctx context.Context, env Env, containerID string, report Progress) (replaced bool, err error)
// unexported, tested:
func buildCreate(old container.InspectResponse, oldImg, newImg image.InspectResponse) (client.ContainerCreateOptions, map[string]*network.EndpointSettings, error) // second value: extra networks to connect before start
func refuseReason(old container.InspectResponse) string // "" when replaceable
```

`Replace` sequence: inspect old → `refuseReason` → inspect old image (by the container's image ID) and new image (by the container's reference); equal IDs → return `false` → stop old → rename old to `<name>-old-<first 8 of ID>` → create under the original name → connect extra networks → start (only if the old one was running) → wait → remove old. Wait: poll every `Poll`; not running → fail; no health check → success after `StartGrace` running; health `healthy` → success; `unhealthy` → fail; still `starting` at `HealthTimeout` → keep it and report a `warning` event. On any failure after the rename: remove the new container, rename the old one back, start it if it was running, return the error.

`buildCreate` rules (spec 6.4, "replacement must be faithful"):
- Drop from the new config whatever equals the old image's value: env entries, labels (key and value), working directory, user, stop signal, health check, exposed ports, volumes.
- Entrypoint equal to the old image's → clear it, and then also clear the command if that equals the old image's. Entrypoint different → keep both untouched (Docker ignores the image's command when the entrypoint is overridden).
- Every port in the port bindings stays in exposed ports.
- Hostname equal to the old container's short ID → cleared.
- Label `com.docker.compose.image`, when present, becomes the new image's ID.
- Each volume mount that is not already in the binds (an anonymous volume) is added as `"<volume name>:<destination>"`.
- Networks: the one named by the network mode goes in the create call; the rest are returned for `NetworkConnect`. Per network copy only fixed-address settings, links, driver options and aliases minus the old short ID; never generated addresses, gateways, MAC or IDs.
- Name: the old name without the leading slash. Image: the old reference.

**Tests:**
- `TestBuildCreateEnv` → container env `[PATH=/usr/bin, APP_ENV=prod, FOO=1]`, old image env `[PATH=/usr/bin, FOO=1]` → `[APP_ENV=prod]`.
- `TestBuildCreateEntrypointCmd` → both inherited → both nil; entrypoint overridden with inherited command → both kept.
- `TestBuildCreateInheritedScalars` → working dir, user, stop signal, health check equal to image → cleared; different → kept.
- `TestBuildCreateLabels` → image label dropped, user label kept, `com.docker.compose.image == newImg.ID`, `com.docker.compose.project` kept.
- `TestBuildCreatePorts` → image exposes `80/tcp` and `443/tcp`, binding on `80/tcp` → exposed ports `{80/tcp}`.
- `TestBuildCreateHostname` → `Hostname == old.ID[:12]` cleared; `"api"` kept.
- `TestBuildCreateAnonymousVolume` → mount `{Type: volume, Name: "abc123", Destination: "/data"}` not in binds → binds contain `"abc123:/data"`; an existing bind `"pgdata:/var/lib/postgresql"` is not duplicated.
- `TestBuildCreateNetworks` → mode `shop_default` plus network `monitoring`: create call holds `shop_default` with alias `api` (short-ID alias removed) and its fixed IPv4 kept, no generated fields; `monitoring` is returned as extra.
- `TestRefuseReason` → network mode `container:abc` → contains `another container's network`; `VolumesFrom` set → contains `volumes from another container`; `Links` set → contains `links`; `AutoRemove` → contains `--rm`; plain container → `""`.
- `TestReplaceHappyPath` → `Calls == ["ContainerStop api-1", "ContainerRename api-1->api-1-old-<id8>", "ContainerCreate api-1", "ContainerStart api-1", "ContainerRemove api-1-old-<id8>"]`, returns `true`.
- `TestReplaceAlreadyCurrent` → no calls, `false`.
- `TestReplaceStartFails` → calls end `"ContainerRemove api-1", "ContainerRename api-1-old-<id8>->api-1", "ContainerStart api-1"`; error returned.
- `TestReplaceExitsDuringGrace`, `TestReplaceUnhealthy` → same rollback.
- `TestReplaceStillStarting` → kept; a `warning` event.
- `TestReplaceStoppedContainer` → created, not started.
- `TestReplaceRefused` → error containing the reason; no calls.
- `TestReplaceExtraNetworks` → `NetworkConnect` recorded before `ContainerStart`.
- `TestPullRestart` → two containers on `app:develop`, pull brings a new image → one `ImagePull app:develop`, then both replaced in order.
- `TestPullRestartUpToDate` → no replacement; an event with `Step == "Already up to date"`.
- `TestPullRestartPullError` → pull stream carrying an error message → error; nothing replaced.
- `TestPullRestartStopsAtFailure` → first replacement fails → second container untouched.
- `TestPullRestartApplies` → false when every reference is digest-pinned.

**Verify:** `./scripts/go test ./internal/actions/...` → `ok`.

---

### Task 8: Logs and shell sessions

**Files:** `internal/streams/logs.go`, `internal/streams/exec.go`, tests beside each.

**Interfaces — produces:**

```go
type LogLine struct {
    Container string `json:"c"`
    Stream    string `json:"s"` // stdout stderr
    Time      string `json:"t"` // RFC 3339 with nanoseconds, "" when absent
    Text      string `json:"l"`
}
type LogOptions struct{ Tail string; Follow bool; Since string } // Tail: "all" or a number
const MaxLine = 256 * 1024
func ReadLogs(ctx context.Context, cli dockerapi.Client, containerID, name string, o LogOptions, emit func(LogLine)) error
func MergedLogs(ctx context.Context, cli dockerapi.Client, containers []services.Container, tail string, emit func(LogLine)) error
func splitTimestamp(raw string) (ts, text string)

var ShellCommand = []string{"/bin/sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi"}
var ErrNoShell, ErrNotRunning error
func StartShell(ctx context.Context, cli dockerapi.Client, containerID string, cols, rows uint) (*ExecSession, error)
func (s *ExecSession) Read(p []byte) (int, error)
func (s *ExecSession) Write(p []byte) (int, error)
func (s *ExecSession) Resize(ctx context.Context, cols, rows uint) error
func (s *ExecSession) ExitCode(ctx context.Context) (int, error)
func (s *ExecSession) Close() error
```

Docker is always asked for timestamps; the browser decides whether to show them. Containers without a terminal use Docker's two-stream framing; those with one are a single raw stream. `MergedLogs` sends the backlog of all containers sorted by time, then live lines as they arrive.

**Tests:**
- `TestSplitTimestamp` → `"2026-11-26T10:00:00.123456789Z hello world"` → (`"2026-11-26T10:00:00.123456789Z"`, `"hello world"`); `"no timestamp"` → (`""`, `"no timestamp"`); timestamp with nothing after → (ts, `""`).
- `TestReadLogsStreams` → framed input with one stdout and one stderr line → two lines with `Stream` `stdout`, `stderr` and `Container == name`.
- `TestReadLogsTTY` → terminal container, raw input → lines with `Stream "stdout"`.
- `TestReadLogsHugeLine` → one 600 000-byte line → at least 3 emitted lines, each at most `MaxLine`, no error.
- `TestReadLogsBinary` → invalid UTF-8 bytes → emitted `Text` is valid UTF-8, no error.
- `TestMergedBacklogSorted` → containers `a` (10:00:01, 10:00:03) and `b` (10:00:02) → order a, b, a.
- `TestStartShell` → exec created with `ShellCommand`, terminal on, env containing `TERM=xterm-256color`, the given size.
- `TestStartShellNotRunning` → stopped container → `ErrNotRunning`.
- `TestExitCodeNoShell` → exec exit code 126 or 127 → `ExitCode` error is `ErrNoShell`.
- `TestResize` → fake records `ExecResize` with the new size.

**Verify:** `./scripts/go test ./internal/streams/...` → `ok`.

---

### Task 9: API core and safety checks

**Files:** `internal/api/server.go`, `internal/api/middleware.go`, `internal/api/session.go`, `internal/api/ui.go`, `internal/api/errors.go`, tests beside each; `internal/webui/webui.go`, `internal/webui/dist/.gitkeep`.

**Interfaces — produces:**

```go
type HostClients interface {
    Client(ctx context.Context, hostID string) (dockerapi.Client, error)
    Test(ctx context.Context, h store.Host) (string, error)
    Invalidate(hostID string)
}
type Deps struct {
    Config  config.Config
    Store   *store.Store
    Hosts   HostClients
    Actions *actions.Registry
    Updates *updates.Checker
    Auth    registryauth.Provider
    UI      fs.FS
    Now     func() time.Time
}
func New(d Deps) http.Handler
func hostAllowed(hostHeader string, allowed []string) bool
func crossSiteOK(r *http.Request) bool
func writeError(w http.ResponseWriter, status int, code, message string) // {"error": message, "code": code}
// webui
func FS() fs.FS // embedded dist
```

Order of checks: panic recovery → allowed hostname → cross-site → session (only when a password is set, only for `/api/` other than `/api/session` and `/api/login`) → route. Routes here: `GET /api/session` → `{name, version, authRequired, authenticated}`; `POST /api/login {password}`; `POST /api/logout`; everything outside `/api/` serves the UI. Session cookie name `<Name>_session`, `HttpOnly`, `SameSite=Strict`, `Path=/`, `Secure` on TLS requests. Sessions live in memory.

Response headers on every response: `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; frame-ancestors 'none'`.

**Tests:**
- `TestHostAllowed` (table) → `localhost:8080`, `127.0.0.1:8080`, `[::1]:8080`, `LOCALHOST` → true; `evil.example`, `localhost.evil.example`, `""` → false; with `panel.lan` allowed, `panel.lan:8080` → true. A request with a refused Host gets 403 and a body naming `DOCKPANEL_ALLOWED_HOSTS`.
- `TestCrossSiteOK` (table, POST unless noted) → `Sec-Fetch-Site: same-origin` true; `none` true; `cross-site` false; `same-site` false; no `Sec-Fetch-Site` with `Origin: http://localhost:8080` and Host `localhost:8080` true; `Origin: http://evil.example` false; neither header true; GET with `cross-site` true; GET with `Upgrade: websocket` and `Origin: http://evil.example` false.
- `TestNoPassword` → `/api/session` → `authRequired false`, `authenticated true`.
- `TestPasswordFlow` → protected route → 401 code `unauthenticated`; wrong password → 401; right → cookie `dockpanel_session` with `HttpOnly` and `SameSite=Strict`; protected route then 200; after logout 401.
- `TestLoginRateLimit` → 10 wrong passwords, the 11th → 429.
- `TestUI` → `/` → `index.html`; `/h/aws/s/c~shop~api` → `index.html`; `/assets/missing.js` → 404; `/api/nope` → 404 JSON with code `not_found`; an `fs.FS` without `index.html` → 200 with text containing `UI not built`.
- `TestSecurityHeaders` → the four headers above on `/` and on `/api/session`.
- `TestPanicRecovered` → a panicking handler → 500 JSON, server keeps serving.

**Verify:** `./scripts/go test ./internal/api/...` → `ok`.

---

### Task 10: API endpoints

**Files:** `internal/api/hosts.go`, `registries.go`, `services.go`, `actions.go`, `containers.go`, `views.go`, tests beside each.

**Interfaces — produces (JSON, camelCase):**

- `HostView {id, name, url, protected, hasTLS}`; `RegistryView {id, address, username, hasPassword}`.
- `ContainerView {id, name, image, imageId, state, health, exitCode, startedAt, restarts}`.
- `ServiceView {key, name, project, image, images, running, total, state, exitCode, startedAt, restarts, update: {status, reason}, actions: [id], containers: [ContainerView]}`; `update.status` is one of the Task 5 statuses or `unchecked`.
- `ServicesResponse {groups: [{project, services: [ServiceView]}], updatesCheckedAt}`.

Routes:

| Method and path | Result |
|---|---|
| `GET /api/hosts` | `[HostView]` |
| `POST /api/hosts`, `PUT /api/hosts/{host}`, `DELETE /api/hosts/{host}` | 201 / 200 `HostView`, 204. Body `{name, url, protected, tls: null \| {ca, cert, key}}`. URL checked with `hosts.ValidateURL`. Update and delete call `Invalidate` and `Updates.Forget`. |
| `POST /api/hosts/test` | 200 `{ok, version, error}`. Body as above plus optional `id`; blank secrets fall back to the stored host's. |
| `GET /api/hosts/{host}/status` | 200 `{ok, version, error}` |
| `GET /api/hosts/{host}/services` | `ServicesResponse`. Starts an update check in the background when the last is older than 15m. |
| `GET /api/hosts/{host}/services/{key}` | `ServiceView` |
| `POST /api/hosts/{host}/updates/check` | forces the check, then `ServicesResponse` |
| `POST /api/hosts/{host}/services/{key}/actions/{action}` | `application/x-ndjson`, one `actions.Event` per line, last has `done: true`. Body `{container?, confirm?}`. |
| `GET /api/actions` | `[actions.Info]` |
| `GET /api/hosts/{host}/containers/{id}/info` | image, image ID, digest, created, started, state, health, restart policy and count, command, ports, mounts, networks, labels, Compose project/service/working dir |
| `GET /api/hosts/{host}/containers/{id}/env` | `{names: [..]}` only |
| `POST /api/hosts/{host}/containers/{id}/env/reveal` | body `{names?: [..]}` (absent = all) → `{values: {NAME: value}}` |
| `GET/POST /api/registries`, `PUT/DELETE /api/registries/{id}` | `RegistryView` |

Error codes: `invalid` 400, `unauthenticated` 401, `confirmation_required` 403, `not_found` 404, `duplicate` 409, `not_applicable` 409, `busy` 409, `host_unreachable` 502.

Action rules: on a protected host `confirm` must equal the service name for every action. One action per host+service at a time. The action runs on a context detached from the request with a 15m limit; a closed browser does not stop it.

**Tests:**
- `TestHostsCRUD` → create 201 with `hasTLS true` and a body that does not contain the PEM key text; bad URL → 400 `invalid`; duplicate name → 409 `duplicate`; rename keeps `id`; update and delete call `Invalidate`; delete → 204; unknown → 404.
- `TestHostTest` → reachable → `{ok: true, version: "27.0"}`; failing → `{ok: false, error: …}`, both 200; with `id` and a blank key the stored key is used.
- `TestServices` → groups with `actions` per service and `update.status "unchecked"` before any check; unknown host → 404; `ContainerList` failing → 502 `host_unreachable` with the Docker message.
- `TestBackgroundUpdateCheck` → first `GET services` triggers a check; a second request shows `update_available` for the service whose registry digest differs.
- `TestCheckNow` → forces a registry call even inside 15 minutes.
- `TestActionStream` → restart on an ordinary host → 200, lines parse as events, last is `{done: true, level: "success"}`, fake shows both restarts.
- `TestActionErrors` → unknown action 404; start on a fully running service → 409 `not_applicable`; a failing restart → 200 whose last line is `{done: true, level: "error"}` carrying the message.
- `TestProtectedHost` → no `confirm` → 403 `confirmation_required`; `confirm: "wrong"` → 403; `confirm: "api"` → runs.
- `TestActionBusy` → while one action on `c~shop~api` is blocked, a second → 409 `busy`; an action on another service proceeds.
- `TestActionSurvivesDisconnect` → cancel the request after the first event → the fake still records the second container's restart.
- `TestEnv` → `GET` body contains names and none of the values; reveal `{names: ["A"]}` → `{values: {A: "1"}}`; no names → all; entry `URL=postgres://u:p@h/db?x=1` → name `URL`, value `postgres://u:p@h/db?x=1`; entry `EMPTY` (no `=`) → name `EMPTY`, value `""`.
- `TestInfo` → ports, mounts, networks and Compose fields present.
- `TestRegistriesCRUD` → password never in a response; `hasPassword true`; update with blank password keeps it; duplicate address → 409.
- `TestActionsList` → four infos in the Task 6 order.

**Verify:** `./scripts/go test ./internal/api/...` → `ok`.

---

### Task 11: Live connections, wiring, image

**Files:** `internal/api/ws.go` (+ test), `cmd/dockpanel/main.go`, `Dockerfile`, `compose.yaml`, `internal/integration/integration_test.go` (build tag `integration`).

**Interfaces — produces:**

- `GET /api/hosts/{host}/containers/{id}/logs/ws?tail=` and `GET /api/hosts/{host}/services/{key}/logs/ws?tail=` — WebSocket; server sends text frames, each a JSON array of `LogLine`, flushed every 50 ms or 200 lines. `tail` is `all` or 1–100000, default 500; anything else → 400 before upgrade.
- `GET /api/hosts/{host}/containers/{id}/logs/download?timestamps=1` — `text/plain`, `Content-Disposition: attachment; filename="<container>.log"`, whole log.
- `GET /api/hosts/{host}/containers/{id}/exec/ws?cols=&rows=` — WebSocket; binary frames carry terminal bytes both ways; text frames are control JSON: browser → `{"type":"resize","cols":N,"rows":N}`; server → `{"type":"exit","code":N,"message":"…"}` then close. Closing the socket ends the session.
- `main`: load config → open store → on a new store, add host `{Name: "local", URL: "unix://" + LocalSocket}` when that socket exists → hosts manager (known hosts file and control directory under the data directory) → checker → `api.New` → serve, with graceful shutdown on SIGINT/SIGTERM. Log a warning when extra hostnames are allowed and no password is set. Flag `-version`.
- `Dockerfile`: stages `golang:1.27-alpine` (build with `-ldflags "-X dockpanel/internal/appinfo.Version=…"`) and `alpine:3` with `openssh-client`, `ca-certificates`; `VOLUME /data`, `EXPOSE 8080`. In this task the image has no UI yet (it answers "UI not built"); Task 12 adds a `node:22-alpine` stage that builds `web/` and copies `web/dist` into `internal/webui/dist` before the Go build.
- `compose.yaml`: port `127.0.0.1:8080:8080`, volume `dockpanel-data:/data`, `/var/run/docker.sock:/var/run/docker.sock`, commented `~/.ssh:/root/.ssh:ro`.

**Tests:**
- `TestLogsWS` → client receives arrays whose lines match the fake's log; `tail=abc` → 400; cross-origin `Origin` → 403.
- `TestMergedLogsWS` → lines from two containers, each with its `c`.
- `TestDownload` → headers above; body has every line; `timestamps=1` prefixes them.
- `TestExecWS` → bytes sent are echoed by the fake exec and received back; a resize frame records `ExecResize`; a no-shell exec → an `exit` control frame whose message contains `no shell`.
- `TestLocalHostAdded` (in `cmd/dockpanel`) → new store plus existing socket path → host `local`; socket missing → no host; existing store → unchanged.
- Integration (real daemon, `make test-integration`): `TestRealList` (a labelled `alpine` container appears as a Compose service), `TestRealRestart`, `TestRealReplace` (tag `dockpanel-it:latest` at one alpine version, create a container with an env, a label, a named volume and an anonymous volume, retag to another alpine version, `Replace` → same name, new image ID, env and label kept, both volumes kept by name, `PATH` not pinned in the new config), `TestRealLogs`, `TestRealShell` (send `echo hi\n`, read `hi`).

**Verify:** `make test` → ok; `make test-integration` → ok; `make build && docker run --rm -d -p 127.0.0.1:18080:8080 -v /var/run/docker.sock:/var/run/docker.sock dockpanel:dev`, then `curl -s localhost:18080/api/hosts` shows `local` and `curl -s localhost:18080/api/hosts/local/services` lists this machine's containers.

---

### Task 12: Frontend foundation

**Files:** `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/index.html`, `web/src/main.tsx`, `web/src/App.tsx`, `web/src/appinfo.ts`, `web/src/api/types.ts`, `web/src/api/client.ts`, `web/src/lib/{confirm,serviceUrl,format,logBuffer,logSearch,ansi}.ts`, `web/src/components/{Layout,HostTabs,StatusDot}.tsx`, `web/src/pages/LoginPage.tsx`, `web/src/styles/*.css`, tests beside each module; `Dockerfile` (add the web build stage).

**Interfaces — produces:**

```ts
// api/types.ts mirrors Task 10 views: HostView, RegistryView, ContainerView, ServiceView, ServicesResponse, ActionInfo, ActionEvent, LogLine
// api/client.ts
api.get<T>(path), api.post<T>(path, body?), api.put<T>(path, body), api.del(path)   // throw ApiError {status, code, message}
streamAction(path: string, body: object, onEvent: (e: ActionEvent) => void): Promise<ActionEvent> // resolves with the final event
readNDJSON(stream: ReadableStream<Uint8Array>, onLine: (value: unknown) => void): Promise<void>
wsURL(path: string): string
// lib
confirmationFor(action: {destructive: boolean}, host: {protected: boolean}): 'none' | 'click' | 'type-name'
servicePath(hostId: string, key: string, o?: {tab?: string; container?: string}): string
parseServiceLocation(pathname: string, search: string): {hostId: string; key: string; tab: string; container: string | null} | null
uptime(startedAt: string, now: Date): string
statusLabel(s: ServiceView, now: Date): string
appendLines<T>(buffer: T[], added: T[], cap?: number): T[]            // cap default 10000
matchLines(lines: {l: string}[], query: string): number[]
splitHighlight(text: string, query: string): {text: string; hit?: boolean}[]
toSegments(text: string): {text: string; color?: string; background?: string; bold?: boolean}[]
```

Routes: `/` (redirect to the first host, or to settings when there are none), `/h/:hostId`, `/h/:hostId/s/:key`, `/settings/hosts`, `/settings/registries`, `/login`. A 401 from the API sends the user to `/login`. Vite dev server proxies `/api` (including WebSockets) to `localhost:8080`. Light and dark themes follow the system setting.

**Tests (Vitest):**
- `confirm` → `(false, ordinary)` → `none`; `(true, ordinary)` → `click`; protected → `type-name` for both.
- `serviceUrl` → `servicePath("aws", "c~shop~api", {tab: "logs", container: "shop-api-1"})` → `"/h/aws/s/c~shop~api?tab=logs&c=shop-api-1"`; parsing it returns the same parts; default tab `logs`; a key with a space survives the round trip; a non-service path → `null`.
- `format` → 3 days → `"3d"`, 5 hours → `"5h"`, 12 minutes → `"12m"`, 40 seconds → `"40s"`; `statusLabel`: running → `"up 3d"`; restarting with 14 restarts → `"restarting ×14"`; stopped with exit 137 → `"exited (137)"`; unhealthy → `"unhealthy"`; partial 1 of 2 → `"1/2 running"`.
- `logBuffer` → appending past the cap keeps the newest 10 000; returns a new array.
- `logSearch` → `matchLines` is case-insensitive; `splitHighlight("an Error here", "error")` → `[{text:"an "},{text:"Error",hit:true},{text:" here"}]`; empty query → one segment; query `"a.b"` does not match `"axb"`.
- `ansi` → `toSegments("\u001b[31mred\u001b[0m <b>")` → first segment text `"red"` with a colour, second text `" <b>"` verbatim.
- `client` → `readNDJSON` with a line split across two chunks yields each object once, in order; a 401 response invokes the unauthenticated handler; `ApiError` carries `code`.
- `HostTabs` → renders one tab per host, a lock on protected ones, the current host marked selected.

**Verify:** `npm --prefix web test` → all pass; `npm --prefix web run build` → `web/dist/index.html` exists; `npm --prefix web run typecheck` → clean; `grep -rn "dangerouslySetInnerHTML\|innerHTML" web/src` → no matches.

---

### Task 13: Services list page

**Files:** `web/src/pages/ServicesPage.tsx`, `web/src/components/{ServicesTable,ActionButtons,ConfirmDialog,ActionProgress,UpdateBadge}.tsx`, `web/src/hooks/{useServices,useActions}.ts`, tests beside each.

**Interfaces — consumes:** Task 12 client and lib. **Produces:** `useActions()` → `{infos: ActionInfo[], run(host: HostView, service: ServiceView, actionId: string, containerId?: string): void, progress}`: decides the confirmation with `confirmationFor`, opens `ConfirmDialog` when needed, calls `streamAction`, and feeds `ActionProgress`. Task 14 reuses it.

Behaviour: spec 6.2 and 6.5. Table grouped by project (`""` shown as "No project"); columns service, containers, count `running/total`, image (or "mixed" with the list as a tooltip), status, actions. Action buttons come from `service.actions` joined with `/api/actions`. Filter box, "hide stopped" toggle, "Check now". Refresh every 5 s while the tab is visible. A failed load shows the error and a Retry button in place of the table. `ActionProgress` lists the events of a running action and stays until dismissed.

**Tests:**
- `ServicesTable` → row shows `2/2`; mixed images show `mixed`; a stopped service has the dimmed class and a Start button; group heading `No project`.
- Filter `"api"` leaves only matching services; "hide stopped" removes stopped rows.
- `ActionButtons` → only the actions listed in `service.actions` are offered.
- `ConfirmDialog` type-name mode → confirm button disabled until the input equals `api`; click mode → enabled at once; Cancel runs nothing.
- `useActions` → ordinary host + restart → `streamAction` called immediately; ordinary host + stop → dialog first; protected host + restart → type-name dialog and the request body carries `confirm: "api"`.
- `ActionProgress` → shows each event's step; an `error` final event stays visible until Dismiss.
- `UpdateBadge` → `update_available` renders the badge; `unknown` renders the grey mark with the reason as its title; `up_to_date`, `pinned`, `unchecked` render nothing.
- Unreachable host → the error text and a Retry button that reloads.

**Verify:** `npm --prefix web test` → pass. Then with the image from Task 11 rebuilt: open `http://localhost:18080`, see this machine's containers grouped, restart one, and watch the progress list.

---

### Task 14: Service page and its tabs

**Files:** `web/src/pages/ServicePage.tsx`, `web/src/panels/{index.ts,LogsPanel,EnvPanel,ShellPanel,InfoPanel}.tsx`, `web/src/components/ContainerList.tsx`, `web/src/hooks/{useLogStream,useShell}.ts`, tests beside each.

**Interfaces — produces:**

```ts
// panels/index.ts — the extension seam for tabs (spec section 9)
type PanelProps = { host: HostView; service: ServiceView; container: ContainerView | null } // null = "All together"
type Panel = { id: string; title: string; supportsAll?: boolean; component: React.ComponentType<PanelProps> }
export const panels: Panel[] // logs, env, shell, info — in that order
```

Behaviour: spec 6.3. Host, service, selected container and tab are read from and written to the URL (`parseServiceLocation` / `servicePath`). Left list of containers with a status dot and per-container restart/stop/start (via `useActions`); "All together" appears only on tabs with `supportsAll`. Logs: tail choice 100/500/2000/all (default 500), follow, timestamps, wrap, search, download link, full screen; lines kept with `appendLines`; colours via `toSegments`. Env: names from the API, values masked as `••••••••`, per-row reveal, Reveal all, filter. Shell: xterm with the fit addon over the exec WebSocket, resize messages, reconnect button, full screen; a stopped container or an `exit` control message shows a message instead of a terminal. Info: the fields of the info endpoint, labels collapsed.

**Tests:**
- `panels` → ids are `["logs","env","shell","info"]`; only `logs` has `supportsAll`.
- `ServicePage` → opening `?tab=env&c=shop-api-2` selects that tab and container; clicking a tab or container updates the URL; "All together" is absent on the Env tab.
- `EnvPanel` → every value shows `••••••••` and the real values are not in the document; clicking reveal on `A` posts `{names: ["A"]}` and shows `1`; "Reveal all" posts without names; the filter narrows by name.
- `LogsPanel` → search `error` marks the matching text; turning timestamps off hides the time column; a line `<script>alert(1)</script>` appears as text and adds no `script` element; with follow off, new lines do not move the scroll position.
- `useLogStream` → batches from the socket are appended; changing the tail reconnects.
- `ShellPanel` (xterm mocked) → a container that is not running shows `not running` and opens no socket; an `exit` control message with code 127 shows `no shell`.
- `ContainerList` → per-container Restart calls `run` with that container's ID.

**Verify:** `npm --prefix web test` → pass. In the rebuilt image: open a service, read live logs, reveal one env value, run `echo hi` in the shell.

---

### Task 15: Settings pages

**Files:** `web/src/pages/{HostsSettings,RegistriesSettings}.tsx`, `web/src/components/{HostForm,RegistryForm}.tsx`, tests beside each.

Behaviour: spec 6.1 and 6.7. Hosts: list, add, edit, delete (with confirmation), "Test connection" showing the Docker version or the error, protected checkbox, TLS fields shown for `tcp://`. Examples of the three connection forms under the URL field. For `ssh://` a note points to the README section on keys. Saved secrets show "saved — leave blank to keep". Registries: list, add, edit, delete.

**Tests:**
- `HostForm` → empty name or URL blocks submit with a message; an API `invalid` error is shown next to the URL field; TLS fields appear only for a `tcp://` URL; editing a host with `hasTLS` shows the "leave blank to keep" hint and submits `key: ""`.
- "Test connection" → success shows `Docker 27.0`; failure shows the error text.
- `RegistryForm` → editing shows the hint for the password and submits `password: ""` when untouched.
- Delete asks before calling the API.

**Verify:** `npm --prefix web test` → pass. In the rebuilt image: add a second host, test it, mark it protected, and see the lock and the type-the-name dialog.

---

### Task 16: Documents, CI, end-to-end check

**Files:** `README.md`, `CONTRIBUTING.md`, `SECURITY.md`, `.github/workflows/ci.yml`.

- **README:** what it is; quick start with `docker run` and `compose.yaml` (port on `127.0.0.1`); adding hosts (socket, TCP+TLS, SSH with keys or agent on Linux, macOS and Windows); registry logins and the ECR token limit; update badge; protected hosts; password and allowed hostnames; what it does not do yet.
- **CONTRIBUTING:** layout; build and test with Docker only; worked examples "add an action" (one Go file + `Default`), "add a tab" (one component + `panels`), "add a registry login type" (`registryauth.Provider`); where the name is defined.
- **SECURITY:** the threat model in plain words (who can reach it, what it can do), safe defaults, how to report a problem.
- **CI:** Go tests, web tests + typecheck + build, image build.

**Verify (the whole product, in the built image):** `make test`, `make test-integration`, `make build` all pass. Run the image; in a browser go through: host tabs → services list → update check → open a service → logs (follow, search, download) → env reveal → shell → restart → pull and restart on a throwaway Compose-labelled service → a protected host's confirmation. Every documented command in the README is run once as written.
