// Search within the log lines already loaded. The query is plain text,
// matched without regard to case; it is never treated as a pattern.

/** Indexes of the lines that contain the query. */
export function matchLines(lines: { l: string }[], query: string): number[] {
  if (query === '') return []
  const needle = query.toLowerCase()
  const hits: number[] = []
  lines.forEach((line, i) => {
    if (line.l.toLowerCase().includes(needle)) hits.push(i)
  })
  return hits
}

export type HighlightPiece = { text: string; hit?: true }

/** Cuts text into pieces, marking each occurrence of the query. */
export function splitHighlight(text: string, query: string): HighlightPiece[] {
  if (query === '' || text === '') return [{ text }]
  const haystack = text.toLowerCase()
  const needle = query.toLowerCase()
  // Lower-casing can change a string's length for a few characters; if it
  // did, positions would not line up, so do not highlight rather than
  // highlight the wrong letters.
  if (haystack.length !== text.length) return [{ text }]

  const pieces: HighlightPiece[] = []
  let from = 0
  for (;;) {
    const at = haystack.indexOf(needle, from)
    if (at < 0) break
    if (at > from) pieces.push({ text: text.slice(from, at) })
    pieces.push({ text: text.slice(at, at + needle.length), hit: true })
    from = at + needle.length
  }
  if (pieces.length === 0) return [{ text }]
  if (from < text.length) pieces.push({ text: text.slice(from) })
  return pieces
}
