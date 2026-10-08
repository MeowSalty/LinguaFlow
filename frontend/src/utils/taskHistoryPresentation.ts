import { h } from 'vue'
import type { DropdownOption } from 'naive-ui'
import { t } from '@/i18n'

export const taskHistoryDeleteOption = (
  canDelete: boolean,
  status: string,
  pending = false,
): DropdownOption => ({
  key: 'delete',
  disabled: !canDelete || pending,
  label: () =>
    h(
      'span',
      {
        class: 'block max-w-[calc(100vw-4rem)] whitespace-normal py-1 leading-5',
      },
      canDelete
        ? t('taskHistory.delete')
        : `${t('taskHistory.delete')} · ${t(
            ['completed', 'failed', 'cancelled'].includes(status)
              ? 'taskHistory.unavailable'
              : 'taskHistory.activeUnavailable',
          )}`,
    ),
})
