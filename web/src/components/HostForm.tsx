import { useState, type FormEvent } from 'react'
import { api, ApiError, paths } from '../api/client'
import type { ConnectionResult, HostView } from '../api/types'
import { Field } from './Field'

type Props = {
  /** The host being edited; absent when adding one. */
  initial?: HostView
  onSaved: (host: HostView) => void
  onCancel: () => void
}

type Errors = { name?: string; url?: string; tls?: string; form?: string }

const KEEP = 'Saved — leave blank to keep.'

/** Adds or edits a host: its name, how to reach it, and whether it is protected. */
export function HostForm({ initial, onSaved, onCancel }: Props) {
  const [name, setName] = useState(initial?.name ?? '')
  const [url, setURL] = useState(initial?.url ?? '')
  const [isProtected, setProtected] = useState(initial?.protected ?? false)
  const [useTLS, setUseTLS] = useState(initial?.hasTLS ?? false)
  const [ca, setCA] = useState('')
  const [cert, setCert] = useState('')
  const [key, setKey] = useState('')
  const [errors, setErrors] = useState<Errors>({})
  const [saving, setSaving] = useState(false)
  const [test, setTest] = useState<{ running: boolean; result: ConnectionResult | null }>({ running: false, result: null })

  const trimmedURL = url.trim()
  const isTCP = trimmedURL.startsWith('tcp://')
  const isSSH = trimmedURL.startsWith('ssh://')
  // The saved certificates never reach the browser, so their fields start
  // empty. Left empty, the server keeps what it has.
  const hasSaved = Boolean(initial?.hasTLS)

  const payload = () => ({
    name: name.trim(),
    url: trimmedURL,
    protected: isProtected,
    tls: isTCP && useTLS ? { ca, cert, key } : null,
  })

  /** Sorts a server error to the field it is about. */
  const place = (e: unknown): Errors => {
    if (!(e instanceof ApiError)) return { form: e instanceof Error ? e.message : String(e) }
    if (e.code === 'duplicate') return { name: e.message }
    if (e.code === 'invalid') return /certificate|key/i.test(e.message) ? { tls: e.message } : { url: e.message }
    return { form: e.message }
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    const missing: Errors = {}
    if (name.trim() === '') missing.name = 'Give the host a name.'
    if (trimmedURL === '') missing.url = 'Enter the connection URL.'
    setErrors(missing)
    if (Object.keys(missing).length > 0) return

    setSaving(true)
    try {
      const saved = initial
        ? await api.put<HostView>(paths.host(initial.id), payload())
        : await api.post<HostView>('/api/hosts', payload())
      onSaved(saved)
    } catch (e) {
      setErrors(place(e))
    } finally {
      setSaving(false)
    }
  }

  async function runTest() {
    if (trimmedURL === '') {
      setErrors({ url: 'Enter the connection URL.' })
      return
    }
    setErrors({})
    setTest({ running: true, result: null })
    try {
      // The ID lets the server use the saved certificates of a host being edited.
      const result = await api.post<ConnectionResult>('/api/hosts/test', { ...payload(), id: initial?.id })
      setTest({ running: false, result })
    } catch (e) {
      setTest({ running: false, result: null })
      setErrors(place(e))
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field label="Name" error={errors.name} hint="Shown on the host’s tab, for example “aws” or “staging”.">
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus={!initial} />
      </Field>

      <Field
        label="Connection URL"
        error={errors.url}
        hint={
          <>
            The same value <code>DOCKER_HOST</code> takes: <code>unix:///var/run/docker.sock</code>,{' '}
            <code>tcp://10.0.0.5:2376</code> or <code>ssh://deploy@server.example.com</code>.
            {isSSH && (
              <>
                {' '}
                SSH uses the keys or agent given to the panel’s container; see “Connecting over SSH” in the README.
              </>
            )}
          </>
        }
      >
        <input
          className="input mono"
          value={url}
          onChange={(e) => {
            setURL(e.target.value)
            setTest({ running: false, result: null })
          }}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
        />
      </Field>

      {isTCP && (
        <div className="field">
          <label className="check">
            <input type="checkbox" checked={useTLS} onChange={(e) => setUseTLS(e.target.checked)} />
            Secure with TLS certificates
          </label>
          {errors.tls && (
            <div className="error" role="alert">
              {errors.tls}
            </div>
          )}
        </div>
      )}
      {isTCP && useTLS && (
        <>
          <Field label="CA certificate" hint={hasSaved ? KEEP : 'PEM text. Verifies the server.'}>
            <textarea className="textarea" rows={3} value={ca} onChange={(e) => setCA(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" spellCheck={false} />
          </Field>
          <Field label="Client certificate" hint={hasSaved ? KEEP : 'PEM text. Identifies the panel to the server.'}>
            <textarea className="textarea" rows={3} value={cert} onChange={(e) => setCert(e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" spellCheck={false} />
          </Field>
          <Field label="Client key" hint={hasSaved ? KEEP : 'PEM text. Stored by the panel and never shown again.'}>
            <textarea className="textarea" rows={3} value={key} onChange={(e) => setKey(e.target.value)} placeholder="-----BEGIN PRIVATE KEY-----" spellCheck={false} />
          </Field>
        </>
      )}

      <div className="field">
        <label className="check">
          <input type="checkbox" checked={isProtected} onChange={(e) => setProtected(e.target.checked)} />
          Protected — ask for the service name to be typed before every action
        </label>
        <div className="hint">For hosts where a mistaken click would hurt, such as production.</div>
      </div>

      {errors.form && (
        <div className="error" role="alert">
          {errors.form}
        </div>
      )}

      <div className="form-actions">
        <button type="submit" className="btn primary" disabled={saving}>
          {initial ? 'Save' : 'Add host'}
        </button>
        <button type="button" className="btn" onClick={() => void runTest()} disabled={test.running}>
          {test.running ? 'Testing…' : 'Test connection'}
        </button>
        <button type="button" className="btn quiet" onClick={onCancel}>
          Cancel
        </button>
        {test.result &&
          (test.result.ok ? (
            <span className="test-result ok" role="status">
              Connected. Docker {test.result.version}.
            </span>
          ) : (
            <span className="test-result bad" role="status">
              {test.result.error}
            </span>
          ))}
      </div>
    </form>
  )
}
