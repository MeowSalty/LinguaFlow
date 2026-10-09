import type { Page } from '@playwright/test'
import type { ApiSchemas } from '../../src/api/client'
import { json, mockApp } from './fixtures.ts'

const timestamp = '2026-09-30T00:00:00Z'
const project = {
  id: 7,
  name: 'Regression workspace',
  owner_user_id: 1,
  source_lang: 'en',
  target_lang: 'zh-Hans',
  glossary_enabled: false,
  created_at: timestamp,
  updated_at: timestamp,
}
const resource = {
  id: 71,
  name: 'welcome.txt',
  path: 'welcome.txt',
  directory: '',
  format: 'txt',
  total_segments: 1,
  translated_segments: 1,
  approved_segments: 0,
  created_at: timestamp,
  updated_at: timestamp,
}
const plan = {
  id: 101,
  name: 'Regression plan',
  scope: 'user',
  owner_user_id: 1,
  profile_id: -1,
  rounds: [
    {
      mode: 'translate',
      backend_id: 101,
      concurrency: 3,
      translate: {
        prompt_template_id: -1,
        batch_size: 10,
        max_words_per_batch: 0,
        fallback_shrink: 1,
      },
    },
  ],
}

export async function regressionApp(
  page: Page,
  options: {
    theme?: 'light' | 'dark'
    populated?: boolean
    upload?: boolean
    glossaryEnabled?: boolean
    rounds?: ApiSchemas['ExecutionRoundConfig'][]
    quickRoundSummary?: ApiSchemas['QuickRoundSummary'][]
  } = {},
) {
  await mockApp(page, { role: 'user', theme: options.theme })
  const errors: string[] = []
  page.on('pageerror', (error) => errors.push(error.message))
  const quickRequests: Record<string, unknown>[] = [],
    segmentWrites: Record<string, unknown>[] = []
  const uploads: string[] = []
  const currentProject = {
    ...project,
    glossary_enabled: options.glossaryEnabled ?? project.glossary_enabled,
    ...(options.upload
      ? { storage_space_id: 1, storage_generation: 0, storage_state: 'active' }
      : {}),
  }
  const currentPlan = { ...plan, rounds: options.rounds ?? plan.rounds }
  let segment = {
    id: 711,
    sub_job_id: 1,
    segment_index: 0,
    source_text: 'Welcome to the workspace.',
    target_text: '欢迎进入工作区。',
    status: 'translated',
    quality_issues: [],
    created_at: timestamp,
    updated_at: timestamp,
  }
  const resources = options.populated
    ? Array.from({ length: 30 }, (_, index) => ({
        ...resource,
        id: 71 + index,
        name: index === 0 ? resource.name : `chapter-${index + 1}.txt`,
        path: index === 0 ? resource.path : `chapter-${index + 1}.txt`,
      }))
    : [resource]
  const segments = () =>
    options.populated
      ? Array.from({ length: 30 }, (_, index) => ({
          ...segment,
          id: 711 + index,
          segment_index: index,
          source_text: index === 0 ? segment.source_text : `Workspace paragraph ${index + 1}.`,
          target_text: index === 0 ? segment.target_text : `工作区段落 ${index + 1}。`,
        }))
      : [segment]
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request(),
      path = new URL(request.url()).pathname.replace('/api/v1', '')
    if (path === '/execution-plan-templates') return json(route, { items: [currentPlan] })
    if (path === '/projects') return json(route, { items: [currentProject] })
    if (path === '/projects/7') return json(route, currentProject)
    if (options.upload && path === '/projects/7/storage')
      return json(route, {
        runtime: { deployment_enabled: true, maintenance: false },
        project_id: 7,
        storage_generation: 0,
        storage_state: 'active',
        binding: { space_id: 1, name: 'Local project storage', scope: 'user' },
        reason_codes: [],
      })
    if (options.upload && path === '/projects/7/resources/precheck')
      return json(route, { items: [{ path: 'header-upload.txt', action: 'create' }] })
    if (options.upload && path === '/projects/7/resources' && request.method() === 'POST') {
      uploads.push(request.postDataBuffer()?.toString() ?? '')
      return json(route, {
        items: [{ path: 'header-upload.txt', action: 'failed', error: '测试上传结果保留' }],
      })
    }
    if (path === '/projects/7/resources/tree')
      return json(route, {
        root: {
          type: 'directory',
          name: '',
          path: '',
          children: resources.map((item) => ({
            type: 'resource',
            name: item.name,
            path: item.path,
            resource: item,
          })),
        },
      })
    if (path === '/projects/7/resources') return json(route, { items: resources })
    if (path === '/projects/7/resources/71/segments/groups')
      return json(route, {
        items: [
          {
            group_key: '',
            group_title: '',
            segment_count: options.populated ? 30 : 1,
            translated_count: options.populated ? 30 : 1,
            approved_count: 0,
          },
        ],
      })
    if (path === '/projects/7/resources/71/segments')
      return json(route, { items: segments(), total: segments().length })
    if (path === '/projects/7/resources/71/segments/711' && request.method() === 'PATCH') {
      const body = request.postDataJSON() as Record<string, unknown>
      segmentWrites.push(body)
      segment = { ...segment, target_text: String(body.target_text), status: 'edited' }
      return json(route, segment)
    }
    if (path === '/quick-translate' && request.method() === 'POST') {
      const body = request.postDataJSON() as Record<string, unknown>
      quickRequests.push(body)
      return json(route, {
        status: 'success',
        source_text: body.source_text,
        target_text: '你好，世界！',
        source_lang: 'en',
        target_lang: 'zh-Hans',
        quality_issues: [],
        round_summary: options.quickRoundSummary,
        usage: { api_calls: 1, input_tokens: 8, output_tokens: 6 },
      })
    }
    return route.fallback()
  })
  return { quickRequests, segmentWrites, errors, uploads, project: currentProject }
}
