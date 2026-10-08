import { expect, type Page } from '@playwright/test'

export async function storageDrawerTab(page: Page, name: '空间' | '连接与授权' | '检测历史') {
  const drawer = page.locator('.n-drawer:visible')
  await drawer.getByRole('tab', { name: name === '空间' ? '存储空间' : name, exact: true }).click()
  return drawer
}

/** Resolve the actual control after navigating its public tab or menu. */
export async function storageControl(page: Page, name: string) {
  const drawer = page.locator('.n-drawer:visible')
  if (name === '撤销授权') {
    await drawer.getByRole('button', { name: '更多操作', exact: true }).click()
    return page.locator('.n-dropdown-option-body').filter({ hasText: name })
  }
  await storageDrawerTab(
    page,
    ['新建空间', '设为只读', '恢复可写'].includes(name)
      ? '空间'
      : ['只读检测', '写入检测'].includes(name)
        ? '检测历史'
        : '连接与授权',
  )
  return drawer.getByRole('button', { name, exact: true })
}

export async function expectStorageControl(page: Page, name: string, enabled: boolean) {
  const control = await storageControl(page, name)
  if (name === '撤销授权') {
    if (enabled) await expect(control).not.toHaveClass(/n-dropdown-option-body--disabled/)
    else await expect(control).toHaveClass(/n-dropdown-option-body--disabled/)
  } else if (enabled) await expect(control).toBeEnabled()
  else await expect(control).toBeDisabled()
  if (name === '撤销授权')
    await page
      .locator('.n-drawer:visible')
      .getByRole('button', { name: '更多操作', exact: true })
      .click()
}

export async function storageAdminTab(page: Page, name: '概览' | '存储连接' | '存储策略') {
  await page.getByRole('tab', { name, exact: true }).click()
}
