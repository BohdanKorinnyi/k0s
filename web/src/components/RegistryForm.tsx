import { useState, type FormEvent } from 'react'
import { api, ApiError } from '../api/client'
import type { RegistryView } from '../api/types'
import { Field } from './Field'

type Props = {
  /** The login being edited; absent when adding one. */
  initial?: RegistryView
  onSaved: (login: RegistryView) => void
  onCancel: () => void
}

/** Adds or edits the login for one image registry. */
export function RegistryForm({ initial, onSaved, onCancel }: Props) {
  const [address, setAddress] = useState(initial?.address ?? '')
  const [username, setUsername] = useState(initial?.username ?? '')
  const [password, setPassword] = useState('')
  const [errors, setErrors] = useState<{ address?: string; form?: string }>({})
  const [saving, setSaving] = useState(false)

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (address.trim() === '') {
      setErrors({ address: 'Enter the registry’s address.' })
      return
    }
    setErrors({})
    setSaving(true)
    const body = { address: address.trim(), username: username.trim(), password }
    try {
      const saved = initial
        ? await api.put<RegistryView>(`/api/registries/${encodeURIComponent(initial.id)}`, body)
        : await api.post<RegistryView>('/api/registries', body)
      onSaved(saved)
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setErrors(e instanceof ApiError && (e.code === 'duplicate' || e.code === 'invalid') ? { address: message } : { form: message })
    } finally {
      setSaving(false)
    }
  }

  return (
    <form className="form" onSubmit={submit} noValidate>
      <Field
        label="Registry address"
        error={errors.address}
        hint={
          <>
            The host part of the image name: <code>ghcr.io</code>, <code>docker.io</code> for Docker Hub,{' '}
            <code>123456789.dkr.ecr.eu-west-1.amazonaws.com</code>.
          </>
        }
      >
        <input className="input mono" value={address} onChange={(e) => setAddress(e.target.value)} spellCheck={false} autoCapitalize="off" autoFocus={!initial} />
      </Field>
      <Field label="Username">
        <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} spellCheck={false} autoCapitalize="off" autoComplete="off" />
      </Field>
      <Field
        label="Password or token"
        hint={
          initial?.hasPassword
            ? 'Saved — leave blank to keep.'
            : 'Stored by the panel and never shown again. A short-lived token, such as one from AWS ECR, works until it expires.'
        }
      >
        <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" />
      </Field>

      {errors.form && (
        <div className="error" role="alert">
          {errors.form}
        </div>
      )}
      <div className="form-actions">
        <button type="submit" className="btn primary" disabled={saving}>
          {initial ? 'Save' : 'Add login'}
        </button>
        <button type="button" className="btn quiet" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}
