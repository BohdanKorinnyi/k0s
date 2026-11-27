import { useCallback, useState } from 'react'
import { APP_NAME } from '../appinfo'

/**
 * A short piece of text remembered in this browser, such as how a list is
 * sorted. If storage is unavailable the preference simply lasts for the visit.
 */
export function useStoredText(name: string, initial = ''): [string, (value: string) => void] {
  const key = `${APP_NAME}.${name}`
  const [value, setValue] = useState(() => {
    try {
      return window.localStorage.getItem(key) ?? initial
    } catch {
      return initial
    }
  })
  const set = useCallback(
    (next: string) => {
      setValue(next)
      try {
        window.localStorage.setItem(key, next)
      } catch {
        // Private mode or disabled storage: nothing to do.
      }
    },
    [key],
  )
  return [value, set]
}

/** A yes/no preference remembered in this browser, such as "hide stopped". */
export function useStoredFlag(name: string, initial = false): [boolean, (value: boolean) => void] {
  const [text, setText] = useStoredText(name, initial ? '1' : '0')
  const set = useCallback((next: boolean) => setText(next ? '1' : '0'), [setText])
  return [text === '1', set]
}
