export type DotTone = 'ok' | 'warn' | 'bad' | 'idle' | 'neutral'

/** A coloured dot. The label is what a screen reader, and a test, reads. */
export function StatusDot({ tone, label }: { tone: DotTone; label: string }) {
  return <span className={`dot ${tone === 'neutral' ? '' : tone}`} role="img" aria-label={label} />
}

/** The dot for a service's or container's state. */
export function toneForState(state: string, health = ''): DotTone {
  if (state === 'restarting' || state === 'unhealthy' || health === 'unhealthy') return 'bad'
  if (state === 'partial' || health === 'starting' || state === 'paused') return 'warn'
  if (state === 'running') return 'ok'
  return 'idle'
}
