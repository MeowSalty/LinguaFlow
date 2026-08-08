import { computed, type ComputedRef } from 'vue'
import { useI18n } from 'vue-i18n'

import type { BatchEventMetadata, SSEEvent } from '@/composables/sseShared'
import { formatTokens } from '@/composables/useWorkspaceUtils'

export interface BatchEventMeta {
  meta: ComputedRef<BatchEventMetadata | null>
  tokenLine: ComputedRef<string>
  glossaryUsedCount: ComputedRef<number>
  glossaryAddedCount: ComputedRef<number>
  statusTagType: ComputedRef<'error' | 'warning' | 'success' | 'default'>
  hasErrorInfo: ComputedRef<boolean>
}

export const useBatchEventMeta = (
  eventGetter: () => SSEEvent,
): BatchEventMeta => {
  const { t } = useI18n()

  const meta = computed<BatchEventMetadata | null>(() => {
    const event = eventGetter()
    if (!event.metadata) return null
    return event.metadata as unknown as BatchEventMetadata
  })

  const tokenLine = computed(() => {
    if (!meta.value) return ''
    if (!meta.value.input_tokens && !meta.value.output_tokens) return ''
    return t('workspace.job.events.batch.tokens', {
      input: formatTokens(meta.value.input_tokens),
      output: formatTokens(meta.value.output_tokens),
    })
  })

  const glossaryUsedCount = computed(() => meta.value?.used_glossary?.length ?? 0)
  const glossaryAddedCount = computed(() => meta.value?.added_glossary?.length ?? 0)
  const statusTagType = computed(() => {
    const status = meta.value?.status
    if (status === 'failed') return 'error'
    if (status === 'partial') return 'warning'
    if (status === 'success') return 'success'
    return 'default'
  })

  const hasErrorInfo = computed(
    () =>
      !!meta.value &&
      (!!meta.value.error_type ||
        !!meta.value.error_message ||
        meta.value.http_status != null ||
        (meta.value.tried_backends?.length != null && meta.value.tried_backends.length > 1) ||
        !!meta.value.shrink_attempted),
  )

  return { meta, tokenLine, glossaryUsedCount, glossaryAddedCount, statusTagType, hasErrorInfo }
}
