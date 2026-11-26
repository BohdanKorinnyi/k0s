/**
 * How an action must be confirmed before it runs.
 *
 * - `none`: runs at once.
 * - `click`: one click on a confirmation dialog.
 * - `type-name`: the service name must be typed.
 *
 * The server enforces the last rule too; this decides what to show.
 */
export type Confirmation = 'none' | 'click' | 'type-name'

export function confirmationFor(action: { destructive: boolean }, host: { protected: boolean }): Confirmation {
  if (host.protected) return 'type-name'
  return action.destructive ? 'click' : 'none'
}
