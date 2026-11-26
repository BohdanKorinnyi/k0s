import { useCallback, useState } from 'react'
import { APP_NAME } from '../appinfo'

/**
 * A yes/no preference remembered in this browser, such as "hide stopped".
 * If storage is unavailable the preference simply lasts for the visit.
 */
export function useStoredFlag(name: string, initial = false): [boolean, (value: boolean) => void] {
  const key = `${APP_NAME}.${name}`
  const [value, setValue] = useState(() => {
    try {
      const stored = window.localStorage.getItem(key)
      return stored === null ? initial : stored === '1'
    } catch {
      return initial
    }
  })
  const set = useCallback(
    (next: boolean) => {
      setValue(next)
      try {
        window.localStorage.setItem(key, next ? '1' : '0')
      } catch {
        // Private mode or disabled storage: nothing to do.
      }
    },
    [key],
  )
  return [value, set]
}
