import Anser from 'anser'

/** A run of log text with one style. */
export type Segment = {
  text: string
  color?: string
  background?: string
  bold?: true
  dim?: true
  italic?: true
  underline?: true
}

const ESC = '\u001b'

/**
 * Turns the colour codes programs put in their logs into styled segments.
 *
 * The result is data, not markup: the caller renders each segment's text as a
 * text node. Log content is never interpreted as HTML.
 */
export function toSegments(text: string): Segment[] {
  if (!text.includes(ESC)) return [{ text }]

  const segments: Segment[] = []
  for (const part of Anser.ansiToJson(text, { use_classes: false, remove_empty: true })) {
    // Anser handles colours; whatever other control sequences are left
    // (cursor movement, line clearing) would only show as garbage.
    const content = stripControlSequences(part.content)
    if (content === '') continue
    const segment: Segment = { text: content }
    if (part.fg) segment.color = `rgb(${part.fg})`
    if (part.bg) segment.background = `rgb(${part.bg})`
    for (const decoration of part.decorations ?? []) {
      if (decoration === 'bold') segment.bold = true
      else if (decoration === 'dim') segment.dim = true
      else if (decoration === 'italic') segment.italic = true
      else if (decoration === 'underline') segment.underline = true
    }
    segments.push(segment)
  }
  return segments.length > 0 ? segments : [{ text: '' }]
}

// CSI sequences: ESC [ parameters letter.
const controlSequence = new RegExp(`${ESC}\\[[0-9;?]*[A-Za-z]`, 'g')

function stripControlSequences(text: string): string {
  return text.replace(controlSequence, '').replaceAll(ESC, '')
}
