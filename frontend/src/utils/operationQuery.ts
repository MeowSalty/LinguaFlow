import type { LocationQuery, LocationQueryRaw } from 'vue-router'
import type { Operation, OperationsQuery, SummaryQuery } from '@/api/operations'

export type OperationLocator = {
  task_type: Operation['task_type']
  task_id: string
  project_id?: number
}
export const operationKey = (task: Pick<Operation, 'task_type' | 'task_id'>): string =>
  `${task.task_type}:${task.task_id}`
export const isTerminalOperation = (status: string): boolean =>
  ['completed', 'failed', 'cancelled'].includes(status)
export const safeTaskNumber = (id: string): number => {
  if (!/^[1-9][0-9]*$/.test(id) || !Number.isSafeInteger(Number(id))) throw new Error('unsafe-id')
  return Number(id)
}
const timestamp = (value: string): bigint => {
  if (
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$/.test(value) ||
    !Number.isFinite(Date.parse(value))
  )
    throw new Error('invalid-date')
  const year = Number(value.slice(0, 4)),
    month = Number(value.slice(5, 7)),
    day = Number(value.slice(8, 10))
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  if (
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > days[month - 1]! ||
    Number(value.slice(11, 13)) > 23
  )
    throw new Error('invalid-date')
  const fraction = /\.(\d+)/.exec(value)?.[1] ?? ''
  return (
    BigInt(Math.floor(Date.parse(value) / 1000)) * 1000000000n + BigInt(fraction.padEnd(9, '0'))
  )
}
export const parseOperationQuery = (
  query: LocationQuery,
  defaultState: 'active' | 'terminal' | 'all' = 'active',
): { filters: OperationsQuery; locator: OperationLocator | null } => {
  const scalar = (key: string): string | undefined => {
    const value = query[key]
    if (value === undefined) return undefined
    if (typeof value !== 'string' || !value) throw new Error('invalid-query')
    return value
  }
  const choice = <T extends string>(key: string, choices: readonly T[]): T | undefined => {
    const value = scalar(key)
    if (value !== undefined && !choices.includes(value as T)) throw new Error('invalid-query')
    return value as T | undefined
  }
  const task_type = choice('task_type', ['translation', 'glossary_sync', 'storage'] as const)
  const status = choice('status', [
    'pending',
    'running',
    'paused',
    'waiting_retry',
    'needs_action',
    'completed',
    'failed',
    'cancelled',
  ] as const)
  const state = choice('state', ['active', 'terminal', 'all'] as const)
  if (status && state) throw new Error('invalid-query')
  const trigger_type = choice('trigger_type', [
    'manual',
    'file_update',
    'glossary_change',
    'web_edit',
  ] as const)
  if (trigger_type && task_type !== 'translation') throw new Error('invalid-query')
  const project = scalar('project_id')
  const project_id = project ? safeTaskNumber(project) : undefined
  const updated_from = scalar('updated_from'),
    updated_before = scalar('updated_before')
  if (updated_from) timestamp(updated_from)
  if (updated_before) timestamp(updated_before)
  if (updated_from && updated_before && timestamp(updated_from) >= timestamp(updated_before))
    throw new Error('invalid-date-range')
  const task_id = scalar('task_id'),
    job_id = scalar('job_id')
  if (task_id && job_id) throw new Error('invalid-link')
  if (job_id && task_type && task_type !== 'translation') throw new Error('invalid-link')
  const id = task_id ?? job_id
  const type = job_id ? 'translation' : task_type
  if (id && (!/^[1-9][0-9]*$/.test(id) || !type || (type !== 'translation' && !project_id)))
    throw new Error('incomplete-link')
  if (id && type === 'storage') safeTaskNumber(id)
  return {
    filters: {
      task_type,
      project_id,
      trigger_type,
      status,
      state: status ? undefined : (state ?? defaultState),
      updated_from,
      updated_before,
    },
    locator: id && type ? { task_type: type, task_id: id, project_id } : null,
  }
}
export const summaryQuery = (query: OperationsQuery): SummaryQuery => ({
  task_type: query.task_type,
  project_id: query.project_id,
  trigger_type: query.trigger_type,
})
export const operationLocation = (
  task: OperationLocator,
): { path: string; query: LocationQueryRaw } => ({
  path: '/operations',
  query: { task_type: task.task_type, task_id: task.task_id, project_id: task.project_id },
})
export const queryKey = (query: object): string =>
  JSON.stringify(
    Object.entries(query)
      .filter(([, value]) => value !== undefined)
      .sort(([a], [b]) => a.localeCompare(b)),
  )
