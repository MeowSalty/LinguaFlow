import { describe, expect, it } from 'vitest'
import { t } from '@/i18n'
import { extractErrorMessage, isTransportFailureMessage } from '../errors'

const FALLBACK = '暂时无法获取登录方式，请重试。'

describe('transport failure copy', () => {
  it.each([
    ['Chromium', 'Failed to fetch'],
    ['Chromium SDK 前缀', 'GET "http://127.0.0.1:8080/api/v1/auth/capabilities": Failed to fetch'],
    ['Safari', 'Load failed'],
    ['Firefox', 'NetworkError when attempting to fetch resource.'],
    ['undici', 'fetch failed'],
    ['离线 WebView', 'The Internet connection appears to be offline.'],
    ['中断的请求', 'The operation was aborted.'],
  ])('把 %s 的原文换成调用方兜底', (_runtime, message) => {
    expect(isTransportFailureMessage(message)).toBe(true)
    expect(extractErrorMessage(new TypeError(message), FALLBACK)).toBe(FALLBACK)
  })

  it('保留本身就是用户文案的界面报错', () => {
    expect(isTransportFailureMessage('无法解析该 EPUB(文件可能已损坏)')).toBe(false)
    expect(extractErrorMessage(new Error('该连接仍被引用，无法停用。'), FALLBACK)).toBe(
      '该连接仍被引用，无法停用。',
    )
  })

  it('读取 Problem Details 的 detail/title,其余形态回退兜底', () => {
    expect(extractErrorMessage({ detail: '认证连接操作失败' }, FALLBACK)).toBe('认证连接操作失败')
    expect(extractErrorMessage({ title: '请求失败' }, FALLBACK)).toBe('请求失败')
    expect(extractErrorMessage('Failed to fetch', FALLBACK)).toBe(FALLBACK)
    expect(extractErrorMessage(null, FALLBACK)).toBe(FALLBACK)
  })

  it('用三处报错现场的中文兜底文案替换传输层原文', () => {
    const failed = new TypeError('Failed to fetch')
    expect(extractErrorMessage(failed, t('authentication.capabilitiesFailed'))).toBe(
      '暂时无法获取登录方式，请重试。',
    )
    expect(extractErrorMessage(failed, t('authAdmin.requestFailed'))).toBe('认证连接操作失败')
    expect(extractErrorMessage(failed, t('localAccount.requestFailed'))).toBe(
      '请求失败，请稍后重试。',
    )
  })
})
