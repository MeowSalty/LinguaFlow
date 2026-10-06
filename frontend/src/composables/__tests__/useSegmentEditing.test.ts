import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope } from 'vue'
import { useSegmentEditing } from '../useSegmentEditing'
import { changeSessionContext } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client'

const state = vi.hoisted(() => ({
  workspace: { updateSegment: vi.fn(), actionError: '' },
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  confirm: vi.fn(async () => true),
}))
vi.mock('naive-ui', () => ({ NInput: {}, useDialog: () => ({}), useMessage: () => state.message }))
vi.mock('../useWorkspaceDraftGuard', () => ({
  useWorkspaceDraftGuard: () => ({ confirmPendingDrafts: state.confirm }),
}))
vi.mock('@/stores/projectWorkspace', () => ({ useProjectWorkspaceStore: () => state.workspace }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const segment = {
  id: 3,
  target_text: 'translated',
  review_comment: 'note',
} as ApiSchemas['Segment']
describe('workspace pending drafts', () => {
  let scope: EffectScope
  beforeEach(() => {
    changeSessionContext('/api/v1', 1, true)
    scope = effectScope()
    state.workspace.updateSegment.mockResolvedValue(segment)
    state.confirm.mockResolvedValue(true)
  })
  afterEach(() => {
    scope.stop()
    vi.resetAllMocks()
  })
  const setup = () => {
    const projectId = ref<number | null>(1)
    const resourceId = ref<number | null>(2)
    return {
      projectId,
      resourceId,
      editing: scope.run(() => useSegmentEditing(projectId, resourceId))!,
    }
  }
  it('tracks actual changes and saves clearing both fields as empty strings', async () => {
    const { editing } = setup()
    await editing.startInlineEdit(segment)
    expect(editing.hasPendingDrafts()).toBe(false)
    editing.inlineEditForm.target_text = ''
    editing.inlineEditForm.comment = ''
    expect(editing.hasPendingDrafts()).toBe(true)
    expect(await editing.savePendingDrafts()).toBe('saved')
    expect(state.workspace.updateSegment).toHaveBeenCalledWith(1, 2, 3, {
      target_text: '',
      comment: '',
    })
    expect(editing.hasPendingDrafts()).toBe(false)
  })
  it('includes independent note drafts and preserves input on save failure', async () => {
    const { editing } = setup()
    await editing.openInlineComment(segment)
    editing.inlineCommentText.value = 'new note'
    state.workspace.updateSegment.mockRejectedValue(new Error('offline'))
    expect(await editing.savePendingDrafts()).toBe('failed')
    expect(editing.inlineCommentText.value).toBe('new note')
    expect(editing.hasPendingDrafts()).toBe(true)
  })
  it('retains new input entered while a save was in flight', async () => {
    const { editing } = setup()
    await editing.startInlineEdit(segment)
    editing.inlineEditForm.target_text = 'first draft'
    let resolve!: (value: typeof segment) => void
    state.workspace.updateSegment.mockReturnValue(
      new Promise<typeof segment>((yes) => {
        resolve = yes
      }),
    )
    const saving = editing.savePendingDrafts()
    editing.inlineEditForm.target_text = 'later draft'
    resolve(segment)
    expect(await saving).toBe('failed')
    expect(editing.inlineEditForm.target_text).toBe('later draft')
    expect(editing.hasPendingDrafts()).toBe(true)
  })
  it('reports stale after a resource switch instead of clearing the new draft', async () => {
    const { editing, resourceId } = setup()
    await editing.startInlineEdit(segment)
    editing.inlineEditForm.target_text = 'old draft'
    let resolve!: (value: typeof segment) => void
    state.workspace.updateSegment.mockReturnValue(
      new Promise<typeof segment>((yes) => {
        resolve = yes
      }),
    )
    const saving = editing.savePendingDrafts()
    resourceId.value = 4
    await editing.startInlineEdit({ ...segment, id: 8 })
    editing.inlineEditForm.target_text = 'new resource draft'
    resolve(segment)
    expect(await saving).toBe('stale')
    expect(editing.inlineEditForm.target_text).toBe('new resource draft')
    expect(state.message.success).not.toHaveBeenCalled()
  })
  it('discards private draft text when the login changes', async () => {
    const { editing } = setup()
    await editing.openInlineComment(segment)
    editing.inlineCommentText.value = 'private note'
    changeSessionContext('/api/v1', 2, true)
    expect(editing.hasPendingDrafts()).toBe(false)
    expect(editing.inlineCommentText.value).toBe('')
  })
  it('canceling a row switch preserves the current draft', async () => {
    const { editing } = setup()
    await editing.startInlineEdit(segment)
    editing.inlineEditForm.target_text = 'draft'
    state.confirm.mockResolvedValue(false)
    await editing.startInlineEdit({ ...segment, id: 8 })
    expect(editing.inlineEditingSegmentId.value).toBe(3)
    expect(editing.inlineEditForm.target_text).toBe('draft')
  })
  it.each(['resource', 'session', 'unmount'])(
    'does not reopen an old row after a %s change while deciding about drafts',
    async (change) => {
      const { editing, resourceId } = setup()
      let resolve!: (value: boolean) => void
      state.confirm.mockReturnValue(
        new Promise<boolean>((yes) => {
          resolve = yes
        }),
      )
      const opening = editing.startInlineEdit(segment)
      if (change === 'resource') resourceId.value = 4
      else if (change === 'session') changeSessionContext('/api/v1', 2, true)
      else scope.stop()
      resolve(true)
      await opening
      expect(editing.inlineEditingSegmentId.value).toBeNull()
      expect(editing.inlineEditForm.target_text).toBe('')
    },
  )
  it('does not advance to an old resource row after an unchanged save', async () => {
    const { editing, resourceId } = setup()
    await editing.startInlineEdit(segment)
    const saving = editing.saveAndEditNext(segment, [segment, { ...segment, id: 8 }])
    resourceId.value = 4
    await saving
    expect(editing.inlineEditingSegmentId.value).toBeNull()
  })
  it('can save new resource drafts before an old resource save returns', async () => {
    const { editing, resourceId } = setup()
    await editing.startInlineEdit(segment)
    editing.inlineEditForm.target_text = 'old draft'
    let resolve!: (value: typeof segment) => void
    state.workspace.updateSegment.mockReturnValueOnce(
      new Promise<typeof segment>((yes) => {
        resolve = yes
      }),
    )
    const oldSave = editing.savePendingDrafts()
    resourceId.value = 4
    await editing.startInlineEdit({ ...segment, id: 8 })
    editing.inlineEditForm.target_text = 'new draft'
    expect(await editing.savePendingDrafts()).toBe('saved')
    expect(state.workspace.updateSegment).toHaveBeenLastCalledWith(1, 4, 8, {
      target_text: 'new draft',
    })
    resolve(segment)
    expect(await oldSave).toBe('stale')
  })
  it('does not report a successful save after the editor unmounts', async () => {
    const { editing } = setup()
    await editing.openInlineComment(segment)
    editing.inlineCommentText.value = 'draft'
    let resolve!: (value: typeof segment) => void
    state.workspace.updateSegment.mockReturnValueOnce(
      new Promise<typeof segment>((yes) => {
        resolve = yes
      }),
    )
    const saving = editing.savePendingDrafts()
    scope.stop()
    resolve(segment)
    expect(await saving).toBe('stale')
    expect(state.message.success).not.toHaveBeenCalled()
  })
})
