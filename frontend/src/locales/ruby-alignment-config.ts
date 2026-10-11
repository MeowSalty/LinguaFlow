import type { RubyRetryValidationError } from '@/utils/execution-plan-config'

export const rubyRetryValidationKeys = {
  concurrency: 'rubyAlignmentConfig.invalidConcurrency',
  batch_size: 'rubyAlignmentConfig.invalidBatchSize',
  max_words_per_batch: 'rubyAlignmentConfig.invalidMaxWords',
  batch_wait_ms: 'rubyAlignmentConfig.invalidBatchWait',
  batch_limits: 'rubyAlignmentConfig.invalidBatchLimits',
} as const satisfies Record<RubyRetryValidationError, string>

export const rubyAlignmentConfig = {
  concurrency: '注音请求并发上限',
  defaultConcurrency: '默认（1）',
  concurrencyHint:
    '单个任务的所有资源与轮次共享此注音请求上限，独立于主轮并发。留空时按默认值 1 执行。修改计划只影响之后创建的任务。',
  invalidConcurrency: '注音并发必须为可安全表示的正整数，或留空使用默认值。',
  batchSettings: '合批设置',
  defaultBatch: '默认单段',
  customBatch: '已自定义',
  batchHint:
    '默认每个注音请求处理 1 段。每批段数控制单个请求的大小，与注音请求并发上限独立；修改计划只影响之后创建的任务。',
  batchSize: '每次注音最多段数',
  defaultBatchSize: '默认（1）',
  batchSizeHint: '0 表示不限段数，需同时设置字词数上限。尾批无需凑满。',
  maxWords: '每次注音最多字词数',
  defaultMaxWords: '默认（0，不限）',
  maxWordsHint:
    '0 表示不限字词数。按实际发送的原文、候选译文、正文区域和待补注音内容计数，不含固定模板；单段超限时独占请求。',
  batchWait: '最大凑批等待时间（毫秒）',
  defaultBatchWait: '默认（25）',
  batchWaitHint: '0 表示立即派发。等待凑批不占用注音请求并发或速率配额。',
  invalidBatchSize: '段数上限必须为可安全表示的非负整数，或留空使用默认值。',
  invalidMaxWords: '字词数上限必须为可安全表示的非负整数，或留空使用默认值。',
  invalidBatchWait: '等待时间必须为 0 到 9223372036854 之间的整数，或留空使用默认值。',
  invalidBatchLimits: '段数上限和字词数上限不能同时为 0；留空分别使用默认值 1 和 0。',
}
