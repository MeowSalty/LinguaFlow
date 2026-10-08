import { t } from '@/i18n'

export type DownloadFileResult = {
  blob: Blob
  filename?: string
}

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
    public readonly problem?: { type?: string; title?: string; detail?: string; status?: number },
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

export const isAccessDenied = (error: unknown): boolean =>
  error instanceof ApiError && (error.status === 403 || error.status === 404)

export const buildRequestFailureError = (
  fallbackMessage: string,
  error?: unknown,
  response?: Response,
): Error => {
  // 检查是否是 Problem 对象 (RFC 7807 格式)
  if (error && typeof error === 'object' && 'title' in error) {
    const problem = error as { type?: string; title?: string; detail?: string; status?: number }
    // 优先使用 detail，其次 title，最后使用 fallbackMessage
    const message = problem.detail || problem.title || fallbackMessage
    return new ApiError(message, response?.status ?? problem.status, problem)
  }

  if (error instanceof Error) {
    return error
  }

  const status = response?.status
  const reason = status
    ? t('api.errors.serverReturned', { status })
    : t('api.errors.requestNotSent')
  return new ApiError(`${fallbackMessage}（${reason}）`, status)
}

export const getContentDispositionFilename = (response?: Response): string | undefined => {
  const contentDisposition = response?.headers.get('content-disposition')

  if (!contentDisposition) {
    return undefined
  }

  const utf8Match = /filename\*=UTF-8''([^;]+)/i.exec(contentDisposition)
  if (utf8Match?.[1]) {
    return decodeURIComponent(utf8Match[1])
  }

  const fallbackMatch = /filename="?([^";]+)"?/i.exec(contentDisposition)
  return fallbackMatch?.[1]
}

export const buildFilesFormData = (files: File[], fieldName: string): FormData => {
  const formData = new FormData()
  files.forEach((file) => {
    formData.append(fieldName, file)
  })
  return formData
}
