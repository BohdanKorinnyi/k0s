# Contributing

Thanks for wanting to add something. The project is built so that the common
additions — an action, a tab, a kind of registry login — are each one small
file plus one line. This page shows how.

## Build and test

Docker is the only requirement. The Makefile uses your local Go and Node when
you have them and containers when you do not.

```sh
make test               # backend and frontend tests; no Docker daemon needed
make test-integration   # backend against your local Docker daemon
make build              # the image, as dc0s:dev
make run                # build and run on http://localhost:8080
```

`make test-integration` creates a few containers, images and volumes named
`dc0s-it-…` on your local daemon and removes them again. It touches
nothing else.

For frontend work with hot reload, run the panel and the dev server side by
side:

```sh
make run                          # terminal 1: the panel on :8080
cd web && npm install && npm run dev   # terminal 2: the frontend on :5173
```

The dev server passes API calls to `http://localhost:8080`; set
`DC0S_API` to point it elsewhere.

## Layout

```
cmd/dc0s        start-up: read settings, wire the parts, serve
internal/
  config             settings from environment variables
  store              the JSON data file: hosts and registry logins
  hosts              a saved host → a live Docker connection (socket, TCP+TLS, SSH)
  dockerapi          the part of the Docker client this project uses
    dockertest       an in-memory fake daemon for tests
  services           containers grouped into services, with a state
  actions            restart, start, stop, pull & restart       ← add actions here
  registryauth       registry logins                            ← add login types here
  updates            "is there a newer image?" with a cache
  streams            logs and shell sessions
  api                HTTP routes, login, safety checks, WebSockets
  webui              the built frontend, embedded in the binary
  integration        tests against a real Docker daemon
web/src/
  api                client and the shapes the server sends
  lib                logic without UI (formatting, search, URLs) and its tests
  components         shared pieces
  panels             the tabs of the service page                ← add tabs here
  pages              one file per page
```

Two rules hold the parts together:

- **Everything that talks to Docker takes `dockerapi.Client`,** not the concrete
  client. Tests run against `dockertest.Fake`. When you need a Docker call the
  project does not use yet, add it to the interface and to the fake.
- **Log lines, env values, names and labels are untrusted text.** They are
  rendered as text nodes. Never use `dangerouslySetInnerHTML`.

Tests come first: write the test, watch it fail, then write the code. Changes
that touch Docker should also be covered in `internal/integration` — the fake
cannot tell you that the real daemon disagrees.

## Add an action

An action is a type in `internal/actions` that implements `Action`. Registered
in `Default`, it is served by the API and drawn by the frontend with no
further changes: on a service's row it appears in the "…" menu, on the service
page as a header button, and — when `ContainerLevel` is set — in each
container's menu.

Here is a *Pause* action. Restart, start and stop share a helper, `lifecycle`,
for "do one Docker call to each container in turn":

```go
// internal/actions/pause.go
package actions

import (
	"context"

	"github.com/moby/moby/client"

	"dc0s/internal/dockerapi"
)

func pauseAction() Action {
	return lifecycle{
		info:    Info{ID: "pause", Label: "Pause", ContainerLevel: true},
		applies: anyUp, // offered while something is running
		verb:    "Pausing",
		do: func(ctx context.Context, cli dockerapi.Client, id string) error {
			_, err := cli.ContainerPause(ctx, id, client.ContainerPauseOptions{})
			return err
		},
	}
}
```

Then:

1. Register it in `Default()` in `actions.go`: `r.Register(pauseAction())`.
2. `ContainerPause` is a Docker call the project does not use yet, so add it to
   `dockerapi.Client` and implement it in `dockertest.Fake` (copy
   `ContainerStop` and set `c.State.Paused`).
3. Test it the way the tests in `actions_test.go` do: put containers on a
   fake, run the action, assert `f.CallLog()`.
4. Three tests name the built-in actions one by one, so that removing one by
   accident is noticed: `TestPullRestartIsRegisteredLast`, `TestActionsList`
   and `TestServices`. Add yours to them.

For anything beyond one call per container, implement `Action` directly;
`pullrestart.go` is the example. Three things to keep:

- `Run` reports each step through `report`, and returns the first error
  without touching the remaining containers.
- Set `Destructive` when the action interrupts a service: it then asks for
  confirmation even on hosts that are not protected.
- `Applies` decides whether the action is offered for a service as it is now.

## Add a tab

A tab is a React component that takes `PanelProps` (the host, the service, and
the chosen container), listed in `web/src/panels/index.ts`.

```tsx
// web/src/panels/ProcessesPanel.tsx
import { useEffect, useState } from 'react'
import { api, paths } from '../api/client'
import type { PanelProps } from './index'

export function ProcessesPanel({ host, container }: PanelProps) {
  const [rows, setRows] = useState<string[][] | null>(null)
  const id = container?.id

  useEffect(() => {
    if (!id) return
    api.get<{ processes: string[][] }>(`${paths.container(host.id, id)}/top`).then((r) => setRows(r.processes))
  }, [host.id, id])

  if (!container) return <div className="panel-message">Choose a container.</div>
  if (!rows) return <div className="panel-message">Loading…</div>
  return (
    <div className="panel">
      <div className="panel-scroll">
        <table className="kv">
          <tbody>
            {rows.map((cells, i) => (
              <tr key={i}>{cells.map((cell, j) => <td key={j} className="value">{cell}</td>)}</tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
```

```ts
// web/src/panels/index.ts
export const panels: Panel[] = [
  // …
  { id: 'processes', title: 'Processes', component: ProcessesPanel },
]
```

The tab now appears on every service page, and `?tab=processes` in the address
opens it. Set `supportsAll: true` if it can show all of a service's containers
at once; it is then given `container: null` when "All together" is chosen.

A tab usually needs data: add a route in `internal/api` (see `containers.go`
for the pattern — `inspectFor` gives you the Docker client and the container),
and a test beside it.

## Add a registry login type

Pulls and update checks get their logins from a `registryauth.Provider`:

```go
type Provider interface {
	Login(ctx context.Context, registryHost string) (Credential, bool, error)
}
```

`Static` serves the logins saved in the UI. To add, say, AWS ECR logins that
refresh themselves, write a provider that answers for ECR hosts and returns
`false` for everything else, then put it beside the saved ones in
`cmd/dc0s/main.go`:

```go
auth := registryauth.Chain{saved, ecr.New(awsConfig)}
```

`Chain` asks each provider in turn and uses the first login found.

## The name

The product is called `dc0s`. A fork that wants another name changes it here:

- `internal/appinfo/appinfo.go` — `Name` and `EnvPrefix`. The data file's
  name, the session cookie and every `DC0S_…` setting follow from these two.
- `web/src/appinfo.ts` — `APP_NAME`; also the `<title>` in `web/index.html`,
  the package name in `web/package.json` (and its lock file), and the setting
  prefix in `web/vite.config.ts`.
- The module path in `go.mod` with the imports that use it, and the folder
  `cmd/dc0s`.
- Two file names written at run time: the temporary-file prefix in
  `internal/store/store.go` and the manifest name in
  `internal/hosts/sshdir.go`.
- The image, volume and network names in `Makefile`, `Dockerfile`,
  `compose.yaml`, `.github/workflows/ci.yml` and `scripts/`.
- The documents and `LICENSE`.

Replacing `dc0s` and `DC0S` throughout, then renaming `cmd/dc0s`, does all of
it; the tests use the same names and say so if something was missed.

## Pull requests

- Keep a change to one thing.
- `make test` passes; `make test-integration` too if you touched anything that
  talks to Docker.
- If you changed behaviour a person can see, say so in the README.
