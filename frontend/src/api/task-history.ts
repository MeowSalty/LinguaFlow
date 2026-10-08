import { apiClient, type ApiClient, type ApiSchemas } from './client-core'
import { ApiError } from './utils'
import { captureSession } from './session-context'
import type { Operation } from './operations'
import { t } from '@/i18n'
import { safeTaskNumber } from '@/utils/operationQuery'

export type TaskHistoryTarget = ApiSchemas['TaskHistoryTarget']
export type TaskHistoryItem = TaskHistoryTarget & {
  project_name?: string
  can_delete: boolean
  status: string
}
export type TaskHistoryResult = TaskHistoryTarget & {
  status: ApiSchemas['TaskHistoryDeleteResult']['status'] | 'unknown'
}
export const taskHistoryKey = (target: TaskHistoryTarget): string =>
  `${target.kind}:${target.id}:${target.project_id}`
export const validTaskHistoryTarget = (target: TaskHistoryTarget): boolean =>
  ['translation', 'glossary_sync'].includes(target.kind) &&
  /^[1-9][0-9]*$/.test(target.id) &&
  Number.isSafeInteger(target.project_id) &&
  target.project_id > 0
export const toTaskHistoryItem = (operation: Operation): TaskHistoryItem | null => {
  if (operation.task_type === 'storage') return null
  const target = {
    kind: operation.task_type,
    id: operation.task_id,
    project_id: operation.project_id,
    project_name: operation.project_name,
    can_delete: operation.can_delete === true,
    status: operation.status,
  }
  return validTaskHistoryTarget(target) ? target : null
}
const codes = {
  settings_conflict: 'conflict',
  task_not_terminal: 'notTerminal',
  task_busy: 'busy',
  task_cleanup_deferred: 'deferred',
  history_unavailable: 'historyUnavailable',
  storage_maintenance: 'blocked',
  storage_operation_in_progress: 'blocked',
  task_recovery_pending: 'blocked',
  recovery_incomplete: 'blocked',
} as const
export class TaskHistoryApiError extends ApiError {
  constructor(
    status?: number,
    public readonly error_code?: string,
  ) {
    const key =
      error_code === 'invalid_contract'
        ? 'contract'
        : (codes[error_code as keyof typeof codes] ??
          (
            {
              400: 'invalid',
              401: 'unauthenticated',
              403: 'forbidden',
              404: 'notFound',
              409: 'busy',
            } as const
          )[status as 400] ??
          'requestFailed')
    super(t(`taskHistoryErrors.${key}`), status)
    this.name = 'TaskHistoryApiError'
  }
}
export const taskHistoryRequestError = (response?: Pick<Response, 'status'>, body?: unknown) => {
  const value = body && typeof body === 'object' ? (body as Record<string, unknown>) : {}
  const raw = value.error_code ?? value.title
  const code = typeof raw === 'string' && /^[a-z][a-z0-9_]{0,95}$/.test(raw) ? raw : undefined
  return new TaskHistoryApiError(response?.status, code)
}
export const taskHistoryErrorMessage = (error: unknown): string => {
  if (error instanceof TaskHistoryApiError) return error.message
  if (error instanceof ApiError)
    return taskHistoryRequestError({ status: error.status ?? 0 }, error.problem).message
  return t('taskHistoryErrors.requestFailed')
}
export const retentionCountLabel = (value: number | null): string =>
  value === null
    ? t('taskHistoryErrors.unknown')
    : Number.isSafeInteger(value) && value >= 0
      ? String(value)
      : t('taskHistoryErrors.imprecise')
export const validRetentionPolicy = (
  value: unknown,
): value is ApiSchemas['TaskRetentionPolicy'] => {
  if (!value || typeof value !== 'object') return false
  const policy = value as ApiSchemas['TaskRetentionPolicy']
  return (
    typeof policy.enabled === 'boolean' &&
    Number.isInteger(policy.retention_days) &&
    policy.retention_days >= 1 &&
    policy.retention_days <= 3650 &&
    Number.isSafeInteger(policy.revision) &&
    policy.revision > 0
  )
}
const signal = (milliseconds: number, extra?: AbortSignal): AbortSignal =>
  AbortSignal.any([
    captureSession().signal,
    AbortSignal.timeout(milliseconds),
    ...(extra ? [extra] : []),
  ])
const record = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value)
const count = (value: unknown): boolean =>
  value === null ||
  (typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value >= 0)
const countFields = (value: unknown, keys: string[]): boolean =>
  record(value) && keys.every((key) => count(value[key]))
const date = (value: unknown): boolean =>
  typeof value === 'string' && Number.isFinite(Date.parse(value))
const previewTypeValid = (value: unknown): boolean =>
  record(value) &&
  countFields(value, ['active', 'terminal', 'missing_anchor', 'not_expired']) &&
  countFields(value.expired, ['blocked', 'busy', 'deletable']) &&
  countFields(value.timing_sources, ['finished_at', 'legacy_anchor', 'missing_anchor']) &&
  countFields(value.terminal_age, [
    'lt_1d',
    'days_1_6',
    'days_7_29',
    'days_30_89',
    'days_90_plus',
    'unknown',
  ]) &&
  countFields(value.deletable_dependencies, [
    'sse_events',
    'job_resources',
    'job_rounds',
    'job_round_segments',
    'credential_job_references',
  ])
export const fetchTaskRetentionPreview = async (
  days: number,
  options?: { signal?: AbortSignal },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TaskRetentionPreview']> => {
  if (!Number.isInteger(days) || days < 1 || days > 3650) throw new TaskHistoryApiError(400)
  const { data, error, response } = await client.POST('/admin/task-retention/preview', {
    body: { retention_days: days },
    signal: signal(15_000, options?.signal),
  })
  if (!response.ok || !data) throw taskHistoryRequestError(response, error)
  if (
    !Number.isSafeInteger(data.policy_revision) ||
    data.policy_revision < 1 ||
    data.retention_days !== days ||
    typeof data.partial !== 'boolean' ||
    !previewTypeValid(data.by_type?.translation) ||
    !previewTypeValid(data.by_type?.glossary_sync) ||
    !Array.isArray(data.incomplete_reasons) ||
    data.incomplete_reasons.some((code) => typeof code !== 'string') ||
    !date(data.as_of) ||
    !date(data.cutoff)
  )
    throw new TaskHistoryApiError(response.status, 'invalid_contract')
  return data
}
export const fetchTaskRetentionStatus = async (
  options?: { signal?: AbortSignal },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TaskRetentionStatus']> => {
  const { data, error, response } = await client.GET('/admin/task-retention/status', {
    signal: signal(15_000, options?.signal),
  })
  if (!response.ok || !data) throw taskHistoryRequestError(response, error)
  const scan = data.last_scan
  const scanValid =
    scan === null ||
    (record(scan) &&
      typeof scan.scan_id === 'string' &&
      Number.isSafeInteger(scan.policy_revision) &&
      scan.policy_revision > 0 &&
      date(scan.as_of) &&
      date(scan.started_at) &&
      date(scan.finished_at) &&
      typeof scan.completed === 'boolean' &&
      typeof scan.error_code === 'string' &&
      countFields(scan, [
        'duration_ms',
        'candidates',
        'deleted',
        'legacy_anchor_count',
        'missing_anchor_count',
      ]) &&
      record(scan.skipped) &&
      Object.values(scan.skipped).every((value) => typeof value === 'number' && count(value)))
  if (
    !validRetentionPolicy(data.task_retention) ||
    data.policy_revision !== data.task_retention.revision ||
    !['disabled', 'idle', 'running', 'blocked', 'error'].includes(data.state) ||
    !Array.isArray(data.reason_codes) ||
    data.reason_codes.some((code) => typeof code !== 'string') ||
    !scanValid ||
    !(data.backlog === null || typeof data.backlog === 'boolean')
  )
    throw new TaskHistoryApiError(response.status, 'invalid_contract')
  return data
}
export const deleteTaskHistory = async (
  target: TaskHistoryTarget,
  client: ApiClient = apiClient,
): Promise<void> => {
  if (!validTaskHistoryTarget(target)) throw new TaskHistoryApiError(400)
  const result =
    target.kind === 'translation'
      ? await client.DELETE('/jobs/{jobId}', {
          params: { path: { jobId: safeTaskNumber(target.id) } },
          signal: signal(15_000),
        })
      : await client.DELETE('/projects/{projectId}/sync-tasks/{taskId}', {
          params: { path: { projectId: target.project_id, taskId: target.id } },
          signal: signal(15_000),
        })
  if (result.response.status !== 204) throw taskHistoryRequestError(result.response, result.error)
}
export const batchDeleteTaskHistory = async (
  targets: TaskHistoryTarget[],
  client: ApiClient = apiClient,
): Promise<{ items: TaskHistoryResult[]; contractError: boolean }> => {
  if (
    !targets.length ||
    targets.length > 100 ||
    targets.some((target) => !validTaskHistoryTarget(target)) ||
    new Set(targets.map(taskHistoryKey)).size !== targets.length
  )
    throw new TaskHistoryApiError(400)
  const items = targets.map(({ kind, id, project_id }) => ({ kind, id, project_id }))
  const { data, error, response } = await client.POST('/operations/batch-delete', {
    body: { items },
    signal: signal(45_000),
  })
  if (!response.ok || !data) throw taskHistoryRequestError(response, error)
  const statuses = [
    'deleted',
    'not_found',
    'forbidden',
    'not_terminal',
    'busy',
    'blocked',
    'deferred',
    'failed',
  ]
  const received = Array.isArray(data.items) ? data.items : []
  let contractError =
    !Array.isArray(data.items) ||
    received.some(
      (item) =>
        !item ||
        !validTaskHistoryTarget(item) ||
        !items.some((target) => taskHistoryKey(target) === taskHistoryKey(item)),
    )
  const outcomes: TaskHistoryResult[] = items.map((target) => {
    const matches = received.filter(
      (item) => item && taskHistoryKey(item) === taskHistoryKey(target),
    )
    if (matches.length !== 1 || !statuses.includes(matches[0]!.status)) contractError = true
    return {
      ...target,
      status:
        matches.length === 1 && statuses.includes(matches[0]!.status)
          ? matches[0]!.status
          : 'unknown',
    }
  })
  return { items: outcomes, contractError }
}
