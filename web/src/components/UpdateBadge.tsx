import type { ServiceView } from '../api/types'

/**
 * Says whether a newer image exists for a service's tag. Nothing is shown
 * when the service is up to date, pinned to an exact image, or not checked
 * yet: silence means "nothing to do".
 */
export function UpdateBadge({ update }: { update: ServiceView['update'] }) {
  if (update.status === 'update_available') {
    return (
      <span className="badge update" title="A newer image is available for this tag. “Pull & restart” fetches it.">
        update
      </span>
    )
  }
  if (update.status === 'unknown') {
    return (
      <span className="badge unknown" title={`Could not check for a newer image: ${update.reason}`}>
        ?
      </span>
    )
  }
  return null
}
