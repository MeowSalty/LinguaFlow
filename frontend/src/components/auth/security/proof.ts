import type { ApiSchemas } from '@/api/client-core'
import { authenticationFailure } from '@/auth/errors'

export type SecurityAction = ApiSchemas['ReauthenticationRequest']['action']
export type LoginMethods = ApiSchemas['LoginMethodsResponse']
export type LoginBinding = ApiSchemas['LoginMethod']
export type AvailableConnection = ApiSchemas['AvailableLoginConnection']

/** 一次性重新认证证明（`takeExternalProof()` 的返回值）。 */
export interface LoginProof {
  action: SecurityAction
  targetId: string
  proof: string
  expiresAt: string
}

export interface ReauthCandidate {
  kind: 'password' | 'binding'
  bindingId: number | null
  label: string
}

/** 解绑操作的 `target_id` 是绑定主键的十进制字符串。 */
export const bindingIdOf = (targetId: string): number | null => {
  if (!/^[1-9]\d*$/.test(targetId)) return null
  const parsed = Number(targetId)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null
}

/**
 * 本次操作允许用来换取证明的登录方式：
 * 删除密码不能用密码本身，解绑不能用目标绑定自己。
 */
export const reauthCandidates = (
  methods: LoginMethods | null,
  action: SecurityAction,
  targetId: string,
): ReauthCandidate[] => {
  if (!methods) return []
  const candidates: ReauthCandidate[] = []
  if (action !== 'password_delete' && methods.password.is_set && methods.password.can_login)
    candidates.push({ kind: 'password', bindingId: null, label: '' })
  const excluded = action === 'unlink' ? bindingIdOf(targetId) : null
  for (const binding of methods.bindings) {
    if (
      !binding.can_reauthenticate ||
      binding.binding_id === excluded ||
      bindingIdOf(String(binding.binding_id)) === null
    )
      continue
    candidates.push({
      kind: 'binding',
      bindingId: binding.binding_id,
      label: binding.display_name,
    })
  }
  return candidates
}

/** 删除密码时密码被策略排除，需要提示原因而不是静默消失。 */
export const passwordReauthBlocked = (
  methods: LoginMethods | null,
  action: SecurityAction,
): boolean => action === 'password_delete' && !!methods?.password.is_set

/** 解绑时目标绑定被策略排除。 */
export const targetReauthBlocked = (
  methods: LoginMethods | null,
  action: SecurityAction,
  targetId: string,
): boolean => {
  if (action !== 'unlink' || !methods) return false
  const wanted = bindingIdOf(targetId)
  return wanted !== null && methods.bindings.some((binding) => binding.binding_id === wanted)
}

/** 证明必须对应当前操作，避免把上一次验证的结果误用到新目标。 */
export const proofMatches = (
  proof: { action: SecurityAction; targetId: string } | null,
  action: SecurityAction,
  targetId: string,
): boolean => !!proof && proof.action === action && proof.targetId === targetId

/** 证明是否仍在有效期内。 */
export const proofIsFresh = (
  proof: { expiresAt: string } | null,
  now: number = Date.now(),
): boolean => {
  if (!proof) return false
  const expiresAt = Date.parse(proof.expiresAt)
  return Number.isFinite(expiresAt) && expiresAt > now
}

/** Recheck capability and canonical target after an external round trip. */
export const actionAllowed = (
  methods: LoginMethods,
  action: SecurityAction,
  targetId: string,
): boolean => {
  switch (action) {
    case 'password_set':
      return targetId === 'password' && !methods.password.is_set && methods.password.can_set
    case 'password_delete':
      return targetId === 'password' && methods.password.is_set && methods.password.can_delete
    case 'unlink':
      return methods.bindings.some(
        (binding) => binding.binding_id === bindingIdOf(targetId) && binding.can_unlink,
      )
    case 'link':
      return methods.available_connections.some(
        (connection) => connection.connection_id === targetId && connection.can_link,
      )
    default:
      return false
  }
}

/**
 * 仅稳定的 reauthentication-required 表示本次证明失效，应重新验证；
 * 其余 403（规则拒绝、授权失败等）不是证明拒绝，不能混用重试验证流程。
 */
export const isProofRejection = (error: unknown): boolean =>
  authenticationFailure(error, 'security') === 'reauthentication-required'
