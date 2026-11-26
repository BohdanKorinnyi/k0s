import type { ServiceView } from '../api/types'

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** How long something has been up, in its largest whole unit: "3d", "5h", "12m", "40s". */
export function uptime(startedAt: string, now: Date): string {
  const elapsed = Math.max(0, now.getTime() - new Date(startedAt).getTime())
  if (elapsed >= DAY) return `${Math.floor(elapsed / DAY)}d`
  if (elapsed >= HOUR) return `${Math.floor(elapsed / HOUR)}h`
  if (elapsed >= MINUTE) return `${Math.floor(elapsed / MINUTE)}m`
  return `${Math.floor(elapsed / SECOND)}s`
}

/** How long ago something happened, in words: "just now", "2 min ago". */
export function relativeTime(at: string, now: Date): string {
  const elapsed = Math.max(0, now.getTime() - new Date(at).getTime())
  if (elapsed < MINUTE) return 'just now'
  if (elapsed < HOUR) return `${Math.floor(elapsed / MINUTE)} min ago`
  if (elapsed < DAY) return `${Math.floor(elapsed / HOUR)} h ago`
  return `${Math.floor(elapsed / DAY)} d ago`
}

/** The short status shown on a service's row. */
export function statusLabel(s: ServiceView, now: Date): string {
  switch (s.state) {
    case 'restarting':
      return s.restarts > 0 ? `restarting ×${s.restarts}` : 'restarting'
    case 'unhealthy':
      return 'unhealthy'
    case 'partial':
      return `${s.running}/${s.total} running`
    case 'stopped':
      return `exited (${s.exitCode})`
    case 'running': {
      const up = s.startedAt ? `up ${uptime(s.startedAt, now)}` : 'up'
      return s.restarts > 0 ? `${up} · restarted ×${s.restarts}` : up
    }
  }
}

/**
 * Splits an image reference into its name and tag for display, so the tag can
 * be set apart. A digest is shortened: nobody reads 64 hex characters.
 */
export function splitImage(ref: string): { name: string; tag: string } {
  const at = ref.indexOf('@')
  if (at >= 0) {
    const digest = ref.slice(at + 1)
    const colon = digest.indexOf(':')
    const short = colon >= 0 ? digest.slice(0, colon + 13) : digest.slice(0, 12)
    return { name: stripTag(ref.slice(0, at)), tag: '@' + short }
  }
  // The tag is after the last colon, unless that colon belongs to a registry
  // port ("localhost:5000/app").
  const colon = ref.lastIndexOf(':')
  if (colon < 0 || ref.indexOf('/', colon) >= 0) return { name: ref, tag: '' }
  return { name: ref.slice(0, colon), tag: ref.slice(colon + 1) }
}

function stripTag(name: string): string {
  const colon = name.lastIndexOf(':')
  if (colon < 0 || name.indexOf('/', colon) >= 0) return name
  return name.slice(0, colon)
}

/** A timestamp for a person: local date and time, without the noise. */
export function formatDateTime(iso: string): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return date.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })
}

/** The time column of a log line: local time of day with milliseconds. */
export function formatLogTime(iso: string): string {
  if (!iso) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  const pad = (n: number, width = 2) => String(n).padStart(width, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`
}
