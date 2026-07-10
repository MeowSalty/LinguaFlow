import type { ApiSchemas } from '@/api/client'
import type { SSEEvent } from '@/composables/sseShared'
import { t } from '@/i18n'

type TranslationJob = ApiSchemas['TranslationJob']
type TranslationJobResource = ApiSchemas['TranslationJobResource']

export interface SyntheticEvent extends SSEEvent {
  synthetic: true
}

export function buildSyntheticEvents(job: TranslationJob): SyntheticEvent[] {
  const events: SyntheticEvent[] = []
  const jobId = job.id

  const makeEvent = (
    type: string,
    level: SSEEvent['level'],
    message: string,
    timestamp: string,
    stage?: string,
  ): SyntheticEvent => ({
    type,
    job_id: jobId,
    level,
    message,
    stage,
    created_at: timestamp,
    seq: 0,
    synthetic: true,
  })

  if (job.started_at) {
    events.push(
      makeEvent(
        'job_started',
        'info',
        t('workspace.job.events.synthetic.jobStarted'),
        job.started_at,
      ),
    )
  }

  if (job.status === 'completed' && job.updated_at) {
    events.push(
      makeEvent(
        'job_completed',
        'info',
        t('workspace.job.events.synthetic.jobCompleted'),
        job.updated_at,
      ),
    )
  } else if (job.status === 'failed' && job.updated_at) {
    events.push(
      makeEvent(
        'job_failed',
        'error',
        job.error_message || t('workspace.job.events.synthetic.jobFailed'),
        job.updated_at,
      ),
    )
  } else if (job.status === 'cancelled' && job.updated_at) {
    events.push(
      makeEvent(
        'job_cancelled',
        'warning',
        t('workspace.job.events.synthetic.jobCancelled'),
        job.updated_at,
      ),
    )
  }

  const resources: TranslationJobResource[] = job.job_resources ?? []
  for (const resource of resources) {
    const resourceLabel = resource.resource?.name || `#${resource.resource_id}`

    if (resource.started_at) {
      events.push(
        makeEvent(
          'resource_started',
          'info',
          t('workspace.job.events.synthetic.resourceStarted', { name: resourceLabel }),
          resource.started_at,
        ),
      )
    }

    if (resource.current_stage && resource.updated_at) {
      events.push(
        makeEvent(
          'stage_start',
          'info',
          t('workspace.job.events.synthetic.stageStart'),
          resource.updated_at,
          resource.current_stage,
        ),
      )
    }

    if (resource.status === 'completed' && resource.updated_at) {
      events.push(
        makeEvent(
          'resource_completed',
          'info',
          t('workspace.job.events.synthetic.resourceCompleted', { name: resourceLabel }),
          resource.updated_at,
        ),
      )
    } else if (resource.status === 'failed' && resource.updated_at) {
      events.push(
        makeEvent(
          'resource_failed',
          'error',
          resource.error_message ||
            t('workspace.job.events.synthetic.resourceFailed', { name: resourceLabel }),
          resource.updated_at,
        ),
      )
    } else if (resource.status === 'cancelled' && resource.updated_at) {
      events.push(
        makeEvent(
          'resource_cancelled',
          'warning',
          t('workspace.job.events.synthetic.resourceCancelled', { name: resourceLabel }),
          resource.updated_at,
        ),
      )
    }
  }

  events.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())

  return events
}
