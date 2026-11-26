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
  `127.0.0.1` and put the panel on a Docker network of its own. The program
  run outside its image listens on `127.0.0.1` unless told otherwise.
- **Known hostnames only.** Requests must be addressed to `localhost`,
  `127.0.0.1`, `::1`, or a name added with `DC0S_ALLOWED_HOSTS`. This
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

## What the defaults do not cover

Without a password, anything that can open a connection to the panel can use
it. The hostname check and the cross-site check stop *browsers* being used
against you; they do not stop a program that connects directly. So:

- **Other accounts on the same machine** can reach `127.0.0.1:8080`.
- **Other containers on the panel's Docker network** can reach it. That is why
  the documented commands give it a network of its own: on Docker's default
  network, every container started with a plain `docker run` could.

If either applies to you, set a password.

With a password, note that the guess limit is shared: anyone who can reach the
panel can keep it at the limit and so keep you from logging in. It is a brake
on guessing, not a defence against someone already on your network.

## If you open it to a network

1. Set `DC0S_PASSWORD` (or `DC0S_PASSWORD_FILE`). The panel logs a
   warning at start if it answers to extra hostnames without one.
2. Put it behind a reverse proxy that provides HTTPS. Without HTTPS the
   password, session cookie, shells and secrets cross the network in the
   clear. The proxy must pass the `Host` header through unchanged and allow
   WebSocket upgrades.
3. Keep it on a network you trust. A VPN is a better answer than a public
   address.

## What is stored, and where

Everything is in the data volume (`/data`):

- `dc0s.json` — hosts, including TLS client keys, and registry logins,
  including passwords, as plain text. The file is readable only by its owner
  (mode 0600), the same level of protection Docker gives its own
  `config.json`. Protect the volume and its backups accordingly.
- `known_hosts` — SSH host keys seen so far. A host key is trusted the first
  time it is seen and must not change afterwards.

SSH private keys are not stored in the data volume. Keys you mount at `/ssh`
are copied, at each start, into the container's own file system (not the
volume), readable only by the panel's user; they go when the container does.
With an agent, the keys never enter the container at all.

Sessions are kept in memory: restarting the panel logs everyone out.

## Reporting a problem

Please report security problems privately, through the repository's
"Report a vulnerability" button (GitHub → Security → Advisories), rather than
in a public issue. Say what you found and how to reproduce it; you will get an
answer as soon as the maintainers can manage.
