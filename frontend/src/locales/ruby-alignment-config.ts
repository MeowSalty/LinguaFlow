export const rubyAlignmentConfig = {
  concurrency: '注音请求并发上限',
  defaultConcurrency: '默认（1）',
  concurrencyHint:
    '单个任务的所有资源与轮次共享此注音请求上限，独立于主轮并发。留空时按默认值 1 执行。修改计划只影响之后创建的任务。',
  invalidConcurrency: '注音并发必须为有限的正整数，或留空使用默认值。',
}
