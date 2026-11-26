import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useEffect, useRef, useState } from 'react'
import { paths, wsURL } from '../api/client'
import type { ContainerView, HostView } from '../api/types'
import { Icon } from '../components/Icon'
import type { PanelProps } from './index'

/** A terminal inside the container. Leaving the tab ends the session. */
export function ShellPanel({ host, container }: PanelProps) {
  // Each attempt is a fresh session with a fresh terminal.
  const [attempt, setAttempt] = useState(0)

  if (!container) return <div className="panel-message">Choose a container to open a shell in.</div>
  if (container.state !== 'running') {
    return (
      <div className="panel">
        <div className="shell-message">
          <p>
            <strong>{container.name}</strong> is not running, so there is no shell to open. Start it first.
          </p>
        </div>
      </div>
    )
  }
  return (
    <ShellSession
      key={`${container.id}:${attempt}`}
      host={host}
      container={container}
      onReconnect={() => setAttempt((n) => n + 1)}
    />
  )
}

type Ended = { code: number; message: string }

type SessionProps = { host: HostView; container: ContainerView; onReconnect: () => void }

function ShellSession({ host, container, onReconnect }: SessionProps) {
  const holder = useRef<HTMLDivElement>(null)
  const [connected, setConnected] = useState(false)
  const [ended, setEnded] = useState<Ended | null>(null)
  const [fullscreen, setFullscreen] = useState(false)

  useEffect(() => {
    const el = holder.current
    if (!el) return

    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, 'SF Mono', SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 13,
      scrollback: 5000,
      theme: { background: '#12151a', foreground: '#d5dae1', cursor: '#d5dae1' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el)
    fit.fit()

    const ws = new WebSocket(
      wsURL(`${paths.container(host.id, container.id)}/exec/ws?cols=${term.cols}&rows=${term.rows}`),
    )
    ws.binaryType = 'arraybuffer'
    const encoder = new TextEncoder()
    const isOpen = () => ws.readyState === WebSocket.OPEN

    ws.onopen = () => {
      setConnected(true)
      term.focus()
    }
    ws.onmessage = (event) => {
      // Binary frames are terminal output; text frames are control messages.
      if (typeof event.data !== 'string') {
        term.write(new Uint8Array(event.data as ArrayBuffer))
        return
      }
      try {
        const control = JSON.parse(event.data) as { type?: string; code?: number; message?: string }
        if (control.type === 'exit') setEnded({ code: control.code ?? 0, message: control.message ?? '' })
      } catch {
        // Not a control message we know; ignore it.
      }
    }
    ws.onclose = () => {
      setConnected(false)
      // If the server did not say how the shell ended, the connection dropped.
      setEnded((prev) => prev ?? { code: -1, message: 'The connection to the shell was lost.' })
    }

    const typed = term.onData((data) => {
      if (isOpen()) ws.send(encoder.encode(data))
    })
    const resized = term.onResize(({ cols, rows }) => {
      if (isOpen()) ws.send(JSON.stringify({ type: 'resize', cols, rows }))
    })
    // Keep the terminal the size of its box, in and out of full screen.
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => fit.fit())
    observer?.observe(el)

    return () => {
      observer?.disconnect()
      typed.dispose()
      resized.dispose()
      ws.onopen = ws.onmessage = ws.onclose = null
      ws.close()
      term.dispose()
    }
  }, [host.id, container.id])

  useEffect(() => {
    if (!fullscreen) return
    // Escape belongs to the shell (vi, less…), so full screen is left with
    // the button, not a key.
  }, [fullscreen])

  return (
    <div className={fullscreen ? 'panel card fullscreen' : 'panel'}>
      <div className="panel-toolbar">
        <span className="log-status">
          <span className={connected ? 'dot ok' : 'dot idle'} />
          {ended ? 'session ended' : connected ? `connected to ${container.name}` : 'connecting…'}
        </span>
        <span className="grow" />
        <button type="button" className="btn small" onClick={onReconnect}>
          {ended ? 'Reconnect' : 'New session'}
        </button>
        <button
          type="button"
          className="btn icon"
          aria-label={fullscreen ? 'Exit full screen' : 'Full screen'}
          title={fullscreen ? 'Exit full screen' : 'Full screen'}
          onClick={() => setFullscreen((v) => !v)}
        >
          <Icon name={fullscreen ? 'shrink' : 'expand'} />
        </button>
      </div>
      {ended && (
        <div className="shell-message" role="status">
          <p>{endedText(ended)}</p>
        </div>
      )}
      {/* The terminal stays mounted so its last output is not thrown away mid-read;
          it is hidden once the session has ended. */}
      <div className="shell" ref={holder} hidden={ended !== null} />
    </div>
  )
}

function endedText(ended: Ended): string {
  if (ended.message) return capitalise(ended.message) + (ended.message.endsWith('.') ? '' : '.')
  return ended.code === 0 ? 'The shell exited.' : `The shell exited with code ${ended.code}.`
}

function capitalise(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1)
}
