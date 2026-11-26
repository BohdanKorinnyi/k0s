# Security

## What the panel can do

Whoever can use the panel can, on every host added to it:

- open a shell in any container,
- read every container's environment variables and logs,
- stop, restart and replace containers.

Access to a Docker daemon is, in practice, root on that host. Treat access to
the panel the same way.

## Who it is for

One person, or one small team that already shares that level of access, on a
machine or private network they control. It has a single optional password and
no user accounts, roles or audit trail. **It is not built to face the
internet.**

## Defaults

- **Local only.** The documented ways to run it publish the port on
  `127.0.0.1`.
- **Known hostnames only.** Requests must be addressed to `localhost`,
  `127.0.0.1`, `::1`, or a name added with `DOCKPANEL_ALLOWED_HOSTS`. This
  stops "DNS rebinding", where a website points a hostname of its own at your
  machine to reach services that listen locally.
- **No cross-site actions.** Requests that change something, and WebSocket
  connections, are refused when another website caused the browser to send
  them.
- **No framing, no outside content.** The page cannot be embedded in another
  page, and loads scripts and connects only to the panel itself.
- **Secrets are write-only.** Registry passwords and TLS client keys are never
  sent to the browser once saved. Environment variable values are sent only
  when you reveal them.
- **Untrusted text stays text.** Log lines, names, labels and env values are
  never interpreted as HTML.
- **Password guessing is slowed.** After ten wrong passwords in a minute,
  logins are refused until the minute has passed.

## If you open it to a network

1. Set `DOCKPANEL_PASSWORD` (or `DOCKPANEL_PASSWORD_FILE`). The panel logs a
   warning at start if it answers to extra hostnames without one.
2. Put it behind a reverse proxy that provides HTTPS. Without HTTPS the
   password, session cookie, shells and secrets cross the network in the
   clear.
3. Keep it on a network you trust. A VPN is a better answer than a public
   address.

## What is stored, and where

Everything is in the data volume (`/data`):

- `dockpanel.json` — hosts, including TLS client keys, and registry logins,
  including passwords, as plain text. The file is readable only by its owner
  (mode 0600), the same level of protection Docker gives its own
  `config.json`. Protect the volume and its backups accordingly.
- `known_hosts` — SSH host keys seen so far. A host key is trusted the first
  time it is seen and must not change afterwards.

SSH private keys are not stored by the panel; it uses the keys or agent you
mount into its container.

Sessions are kept in memory: restarting the panel logs everyone out.

## Reporting a problem

Please report security problems privately, through the repository's
"Report a vulnerability" button (GitHub → Security → Advisories), rather than
in a public issue. Say what you found and how to reproduce it; you will get an
answer as soon as the maintainers can manage.
