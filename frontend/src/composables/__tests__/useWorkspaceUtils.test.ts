import { describe, expect, it, vi } from 'vitest'
import type { ApiSchemas } from '@/api/client'
import {
  calculateJobETA,
  calculateJobSpeed,
  getJobProgress,
  getJobProgressText,
  getJobStatusLabel,
  statusTagType,
} from '../useWorkspaceUtils'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const job: ApiSchemas['Job'] = {
  id: 1,
  project_id: 7,
  execution_plan_id: 1,
  status: 'pausing',
  trigger_type: 'manual',
  can_delete: false,
  finished_at: null,
  started_at: '2026-01-01T00:00:00Z',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:30Z',
  progress: {
    total_resources: 1,
    completed_resources: 0,
    failed_resources: 0,
    progress_total: 20,
    progress_completed: 5,
    stages: {
      main_requests: 0,
      pending_alignment: 0,
      alignment_requests: 0,
      saving_requests: 0,
      ready_to_commit: 0,
      confirmed_work: 4,
      unknown_requests: 0,
      draining_requests: 0,
      as_of: '2026-01-01T00:00:30Z',
    },
  },
}

describe('pausing job presentation', () => {
  it('waits for the server pause state even when all observed requests have drained', () => {
    expect(getJobStatusLabel(job.status)).toBe('workspace.job.status.pausing')
    expect(getJobProgressText(job)).toBe('workspace.job.progress.pausing')
    expect(statusTagType(job.status)).toBe('warning')
    expect(getJobProgress(job)).toBe(25)
    expect(job.status).toBe('pausing')
  })

  it('suppresses completion and speed estimates during safe pause', () => {
    expect(calculateJobETA({ ...job, status: 'running' })).toBeGreaterThan(0)
    expect(calculateJobETA(job)).toBeNull()
    expect(calculateJobSpeed(job)).toBeNull()
  })
})
