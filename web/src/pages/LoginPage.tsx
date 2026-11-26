import { useState, type FormEvent } from 'react'
import { api, ApiError } from '../api/client'
import { APP_NAME } from '../appinfo'

/** Asks for the panel's password. Shown in place of any page until it is given. */
export function LoginPage({ onLoggedIn }: { onLoggedIn: () => void }) {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api.post('/api/login', { password })
      onLoggedIn()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not log in.')
      setPassword('')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="center-screen">
      <form className="card login" onSubmit={submit}>
        <h1>{APP_NAME}</h1>
        <p>Enter the password to continue.</p>
        <label className="visually-hidden" htmlFor="password">
          Password
        </label>
        <input
          id="password"
          className="input"
          type="password"
          autoComplete="current-password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="Password"
        />
        {error && (
          <div className="error" role="alert">
            {error}
          </div>
        )}
        <button className="btn primary" type="submit" disabled={busy || password === ''}>
          {busy ? 'Checking…' : 'Log in'}
        </button>
      </form>
    </div>
  )
}
