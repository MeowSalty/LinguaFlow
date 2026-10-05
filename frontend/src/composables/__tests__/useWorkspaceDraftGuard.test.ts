import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, type EffectScope, type VNode } from 'vue'
import { useWorkspaceDraftGuard, type DraftSaveResult } from '../useWorkspaceDraftGuard'
import { changeSessionContext } from '@/api/session-context'

const state = vi.hoisted(() => ({ warning: vi.fn(), destroy: vi.fn() }))
vi.mock('naive-ui', () => ({
  NButton: 'button',
  NSpace: {},
  useDialog: () => ({ warning: state.warning }),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
describe('draft decision lifecycle', () => {
  let scope: EffectScope
  beforeEach(() => {
    changeSessionContext('/api/v1', 1, true)
    scope = effectScope()
    state.warning.mockReturnValue({ destroy: state.destroy })
  })
  afterEach(() => {
    scope.stop()
    vi.resetAllMocks()
  })
  const setup = () => {
    const drafts = {
      hasPendingDrafts: () => true,
      savePendingDrafts: vi.fn(async (): Promise<DraftSaveResult> => 'saved'),
      discardPendingDrafts: vi.fn(),
    }
    const guard = scope.run(() => useWorkspaceDraftGuard(() => drafts))!
    return { drafts, guard }
  }
  const buttons = (): VNode[] => {
    const action = state.warning.mock.calls[0]![0].action() as VNode
    return (action.children as { default: () => VNode[] }).default()
  }
  it('cannot discard or cancel after save starts and renders all buttons disabled', async () => {
    const { drafts, guard } = setup()
    let resolve!: (result: DraftSaveResult) => void
    drafts.savePendingDrafts.mockReturnValue(
      new Promise<DraftSaveResult>((yes) => {
        resolve = yes
      }),
    )
    const decision = guard.confirmPendingDrafts()
    const original = buttons()
    const saving = original[2]!.props!.onClick()
    original[1]!.props!.onClick()
    original[0]!.props!.onClick()
    expect(buttons().every((button) => button.props!.disabled)).toBe(true)
    expect(drafts.discardPendingDrafts).not.toHaveBeenCalled()
    expect(state.destroy).not.toHaveBeenCalled()
    resolve('saved')
    await saving
    expect(await decision).toBe(true)
  })
  it('session invalidation destroys the dialog and resolves navigation false', async () => {
    const { drafts, guard } = setup()
    const decision = guard.confirmPendingDrafts()
    const original = buttons()
    changeSessionContext('/api/v1', 2, true)
    expect(await decision).toBe(false)
    original[1]!.props!.onClick()
    expect(drafts.discardPendingDrafts).not.toHaveBeenCalled()
    expect(state.destroy).toHaveBeenCalledTimes(1)
  })
  it('disposing an editor resolves an outstanding decision without hanging', async () => {
    const { guard } = setup()
    const decision = guard.confirmPendingDrafts()
    scope.stop()
    expect(await decision).toBe(false)
    expect(state.destroy).toHaveBeenCalledTimes(1)
  })
  it('a thrown save failure refuses navigation', async () => {
    const { drafts, guard } = setup()
    drafts.savePendingDrafts.mockRejectedValue(new Error('save failed'))
    const decision = guard.confirmPendingDrafts()
    await buttons()[2]!.props!.onClick()
    expect(await decision).toBe(false)
  })
})
