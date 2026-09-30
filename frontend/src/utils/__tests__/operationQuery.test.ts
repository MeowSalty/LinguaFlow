import { describe, expect, it } from 'vitest'
import {
  operationKey,
  operationLocation,
  parseOperationQuery,
  queryKey,
  safeTaskNumber,
  summaryQuery,
} from '../operationQuery'

describe('operations URLs and identifiers', () => {
  it('keeps colliding translation and sync identifiers distinct', () => {
    expect(operationKey({ task_type: 'translation', task_id: '42' })).not.toBe(
      operationKey({ task_type: 'glossary_sync', task_id: '42' }),
    )
  })
  it('keeps string identifiers intact until a numeric API boundary', () => {
    const { locator } = parseOperationQuery({
      task_type: 'translation',
      task_id: '9007199254740993',
    })
    expect(locator?.task_id).toBe('9007199254740993')
    expect(() => safeTaskNumber(locator!.task_id)).toThrow()
    expect(safeTaskNumber('9007199254740991')).toBe(Number.MAX_SAFE_INTEGER)
  })
  it.each(['0', '-1', '01', '1.2', '1e3', 'Infinity', '9007199254740992'])(
    'rejects unsafe numeric identifier %s',
    (value) => {
      expect(() => safeTaskNumber(value)).toThrow()
    },
  )
  it('uses explicit state over preferences and supports legacy translation links', () => {
    expect(parseOperationQuery({}, 'terminal').filters.state).toBe('terminal')
    expect(parseOperationQuery({ state: 'active' }, 'terminal').filters.state).toBe('active')
    expect(parseOperationQuery({ status: 'failed' }, 'terminal').filters.state).toBeUndefined()
    expect(parseOperationQuery({ job_id: '9' }).locator).toEqual({
      task_type: 'translation',
      task_id: '9',
      project_id: undefined,
    })
  })
  it.each([
    { state: 'active', status: 'running' },
    { trigger_type: 'manual' },
    { task_type: 'glossary_sync', trigger_type: 'manual' },
    { task_type: 'glossary_sync', task_id: '1' },
    { task_id: '1' },
    { task_type: 'translation', task_id: '1', job_id: '2' },
    { task_type: 'translation', state: ['active', 'terminal'] },
    { task_type: 'unknown' },
    { updated_from: 'not-a-date' },
  ])('rejects incompatible or ambiguous URL parameters %o', (query) => {
    expect(() => parseOperationQuery(query)).toThrow()
  })
  it('retains original submillisecond timestamps and compares at full precision', () => {
    const updated_from = '2026-09-30T00:00:00.123456788Z'
    const updated_before = '2026-09-30T00:00:00.123456789Z'
    const { filters } = parseOperationQuery({ updated_from, updated_before })
    expect(filters.updated_from).toBe(updated_from)
    expect(filters.updated_before).toBe(updated_before)
    expect(() =>
      parseOperationQuery({ updated_from: updated_before, updated_before: updated_from }),
    ).toThrow()
    expect(() => parseOperationQuery({ updated_from, updated_before: updated_from })).toThrow()
  })
  it('sends only allowed dimensions to summaries', () => {
    expect(
      summaryQuery({
        task_type: 'translation',
        project_id: 7,
        trigger_type: 'manual',
        state: 'active',
        updated_from: '2026-09-30T00:00:00Z',
        cursor: 'next',
      }),
    ).toEqual({ task_type: 'translation', project_id: 7, trigger_type: 'manual' })
  })
  it('generates complete sync deep links and stable cache keys', () => {
    expect(operationLocation({ task_type: 'glossary_sync', task_id: '8', project_id: 7 })).toEqual({
      path: '/operations',
      query: { task_type: 'glossary_sync', task_id: '8', project_id: 7 },
    })
    expect(queryKey({ project_id: 7, state: 'active', cursor: undefined })).toBe(
      queryKey({ state: 'active', project_id: 7 }),
    )
  })
})
