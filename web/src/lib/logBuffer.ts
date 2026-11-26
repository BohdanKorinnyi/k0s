/** The most log lines kept in the browser at once. Older ones are dropped. */
export const LOG_BUFFER_CAP = 10_000

/**
 * Adds lines to a buffer and keeps only the newest `cap`. Always returns a new
 * array, so React sees a change.
 */
export function appendLines<T>(buffer: T[], added: T[], cap: number = LOG_BUFFER_CAP): T[] {
  const total = buffer.length + added.length
  if (total <= cap) return [...buffer, ...added]
  if (added.length >= cap) return added.slice(added.length - cap)
  return [...buffer.slice(total - cap), ...added]
}
