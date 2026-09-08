export interface ApiProblem {
  status?: number
  title?: string
  detail?: string
}

/**
 * 从未知错误对象中提取用户可读的消息：
 * Error 实例取 message，否则按 Problem Details 结构取 detail/title，最后回退到 fallback。
 */
export const extractErrorMessage = (error: unknown, fallback: string): string => {
  if (error instanceof Error && error.message) {
    return error.message
  }
  if (error && typeof error === 'object') {
    const problem = error as ApiProblem
    return problem.detail || problem.title || fallback
  }
  return fallback
}
