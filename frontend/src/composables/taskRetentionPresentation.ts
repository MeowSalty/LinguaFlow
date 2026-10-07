import { t } from '@/i18n'
import { formatDateTime } from '@/utils/datetime'

const reasonKeys = {
  maintenance: 'maintenance',
  storage_maintenance: 'maintenance',
  recovery_incomplete: 'recovery_incomplete',
  task_recovery_pending: 'recovery_incomplete',
  settings_unavailable: 'settings_unavailable',
  anchor_initialization_failed: 'anchor_initialization_failed',
  anchor_init_failed: 'anchor_initialization_failed',
  candidate_read_failed: 'candidate_read_failed',
  candidates_failed: 'candidate_read_failed',
  candidate_query_failed: 'candidate_read_failed',
  cleanup_failed: 'cleanup_failed',
  task_cleanup_failed: 'cleanup_failed',
  timeout: 'timeout',
  deadline_exceeded: 'timeout',
  query_timeout: 'timeout',
  evaluation_budget_exhausted: 'timeout',
  busy: 'busy',
  task_busy: 'busy',
  blocked: 'blocked',
  deferred: 'deferred',
  task_cleanup_deferred: 'deferred',
  failed: 'failed',
  not_terminal: 'not_terminal',
  not_found: 'not_found',
  forbidden: 'forbidden',
} as const
/** Unknown service values never become user-visible raw strings. */
export const retentionReason = (code: string) =>
  t(`taskRetention.reasons.${reasonKeys[code as keyof typeof reasonKeys] ?? 'unknown'}`)
export const retentionDate = (value: string) =>
  Number.isFinite(Date.parse(value))
    ? formatDateTime(value, {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        timeZoneName: 'short',
      })
    : t('taskRetention.unknown')
