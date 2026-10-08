import { h, onScopeDispose, ref } from 'vue'
import { NButton, NSpace, useDialog } from 'naive-ui'
import { t } from '@/i18n'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'

export type DraftSaveResult = 'saved' | 'failed' | 'stale'
export interface WorkspaceDrafts {
  hasPendingDrafts: () => boolean
  savePendingDrafts: () => Promise<DraftSaveResult>
  discardPendingDrafts: () => void
}

/** One shared three-way decision for navigation, downloads and snapshot creation. */
export function useWorkspaceDraftGuard(getDrafts: () => WorkspaceDrafts | null | undefined) {
  const dialog = useDialog()
  let pending: Promise<boolean> | null = null
  let dismiss: (() => void) | null = null
  const stopSessionListener = onSessionChange(() => dismiss?.())
  onScopeDispose(() => {
    dismiss?.()
    stopSessionListener()
  })
  const confirmPendingDrafts = (): Promise<boolean> => {
    if (pending) return pending
    const drafts = getDrafts()
    if (!drafts?.hasPendingDrafts()) return Promise.resolve(true)
    pending = new Promise<boolean>((resolve) => {
      const session = captureSession()
      let settled = false
      const saving = ref(false)
      const finish = (allowed: boolean) => {
        if (settled) return
        settled = true
        resolve(allowed)
        instance.destroy()
      }
      dismiss = () => finish(false)
      const instance = dialog.warning({
        title: t('sourceStorage.drafts.title'),
        content: t('sourceStorage.drafts.description'),
        maskClosable: false,
        closeOnEsc: false,
        onClose: () => {
          if (saving.value) return false
          finish(false)
        },
        action: () =>
          h(
            NSpace,
            { justify: 'end' },
            {
              default: () => [
                h(
                  NButton,
                  {
                    disabled: saving.value,
                    onClick: () => {
                      if (!saving.value) finish(false)
                    },
                  },
                  { default: () => t('common.cancel') },
                ),
                h(
                  NButton,
                  {
                    disabled: saving.value,
                    onClick: () => {
                      if (saving.value || settled || !isSessionCurrent(session)) return
                      drafts.discardPendingDrafts()
                      finish(true)
                    },
                  },
                  { default: () => t('sourceStorage.drafts.discard') },
                ),
                h(
                  NButton,
                  {
                    type: 'primary',
                    disabled: saving.value,
                    onClick: async () => {
                      if (saving.value || settled || !isSessionCurrent(session)) return
                      saving.value = true
                      try {
                        finish(
                          (await drafts.savePendingDrafts()) === 'saved' &&
                            isSessionCurrent(session),
                        )
                      } catch {
                        finish(false)
                      } finally {
                        saving.value = false
                      }
                    },
                  },
                  { default: () => t('sourceStorage.drafts.save') },
                ),
              ],
            },
          ),
      })
    }).finally(() => {
      pending = null
      dismiss = null
    })
    return pending
  }
  return { confirmPendingDrafts }
}
