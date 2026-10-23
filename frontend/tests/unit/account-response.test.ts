import { expect, it, vi } from 'vitest'
import { changeCurrentUserPassword } from '@/api/account'
import type { ApiClient } from '@/api/client-core'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))

it('does not report a successful password change for an empty error response', async () => {
  const client = {
    PUT: vi.fn().mockResolvedValue({ response: new Response(null, { status: 503 }) }),
  } as unknown as ApiClient
  await expect(
    changeCurrentUserPassword(
      { current_password: 'original', new_password: 'new-password' },
      client,
    ),
  ).rejects.toMatchObject({ status: 503 })
})
