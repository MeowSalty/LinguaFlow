type DraftReader = {
  projectId: () => number | null
  resourceId: () => number | null
  pending: () => boolean
}
const readers = new Set<DraftReader>()

/** Background publication refreshes must not replace a newly started editor draft. */
export const registerWorkspaceDraftReader = (reader: DraftReader): (() => void) => {
  readers.add(reader)
  return () => readers.delete(reader)
}
export const hasWorkspaceDrafts = (projectId: number, resourceId: number): boolean =>
  [...readers].some(
    (reader) =>
      reader.projectId() === projectId && reader.resourceId() === resourceId && reader.pending(),
  )
