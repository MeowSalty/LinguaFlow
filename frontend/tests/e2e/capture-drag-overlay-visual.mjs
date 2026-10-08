import { chromium } from '@playwright/test'
import { mkdir } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { regressionApp } from './workspace-regression-fixtures.ts'

// 对已构建的 production preview 运行：
// node --experimental-strip-types tests/e2e/capture-drag-overlay-visual.mjs
const output = fileURLToPath(new URL('../artifacts/drag-overlay/', import.meta.url))
await mkdir(output, { recursive: true })
const browser = await chromium.launch()

const OVERLAY_TEXT = '松开鼠标，上传到当前目录'
const overlayText = (page) => page.getByText(OVERLAY_TEXT, { exact: true })

// 合成 window 级 DragEvent；kind='files' 时携带 File（types 含 'Files'）
const fireDrag = (page, type, kind = 'files') =>
  page.evaluate(
    ([type, kind]) => {
      const dt = new DataTransfer()
      if (kind === 'files') dt.items.add(new File(['hello'], 'hello.txt', { type: 'text/plain' }))
      else dt.setData('text/plain', '普通文本拖拽')
      window.dispatchEvent(
        new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }),
      )
    },
    [type, kind],
  )

async function captureTheme(theme) {
  const context = await browser.newContext({
    viewport: { width: 1524, height: 984 },
    deviceScaleFactor: 2,
  })
  const page = await context.newPage()
  await regressionApp(page, { theme, upload: true })
  await page.goto('http://127.0.0.1:4173/projects/7')
  await page.getByText('welcome.txt').first().waitFor({ timeout: 20000 })
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${output}${theme}-1-baseline.png` })

  // 文件拖入 → 全屏遮罩 + 中央卡片
  await fireDrag(page, 'dragenter')
  await fireDrag(page, 'dragover')
  await overlayText(page).waitFor({ timeout: 3000 })
  await page.waitForTimeout(400)
  await page.screenshot({ path: `${output}${theme}-2-overlay.png` })

  // 子元素间往返抖动（enter/leave 成对触发）→ 遮罩应保持稳定
  for (let i = 0; i < 3; i += 1) {
    await fireDrag(page, 'dragenter')
    await fireDrag(page, 'dragleave')
  }
  await page.waitForTimeout(400)
  console.log(
    `${theme} counter-stability:`,
    (await overlayText(page).isVisible()) ? 'PASS' : 'FAIL',
  )

  // 拖离窗口 → 遮罩消失
  await fireDrag(page, 'dragleave')
  await page.waitForTimeout(500)
  console.log(`${theme} hide-on-leave:`, !(await overlayText(page).isVisible()) ? 'PASS' : 'FAIL')

  // 纯文本拖拽 → 不触发遮罩
  await fireDrag(page, 'dragenter', 'text')
  await fireDrag(page, 'dragover', 'text')
  await page.waitForTimeout(400)
  console.log(
    `${theme} text-drag-ignored:`,
    !(await overlayText(page).isVisible()) ? 'PASS' : 'FAIL',
  )

  // 重新拖入并释放 → 上传链路触发（预检请求 + 遮罩复位）
  let precheckRequested = false
  const onRequest = (request) => {
    if (request.url().includes('/resources/precheck')) precheckRequested = true
  }
  page.on('request', onRequest)
  await fireDrag(page, 'dragenter')
  await fireDrag(page, 'dragover')
  await page.evaluate(() => {
    const dt = new DataTransfer()
    dt.items.add(new File(['hello'], 'hello.txt', { type: 'text/plain' }))
    window.dispatchEvent(
      new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer: dt }),
    )
  })
  await page.waitForTimeout(1200)
  page.off('request', onRequest)
  console.log(`${theme} drop-fires-precheck:`, precheckRequested ? 'PASS' : 'FAIL')
  console.log(
    `${theme} overlay-resets-after-drop:`,
    !(await overlayText(page).isVisible()) ? 'PASS' : 'FAIL',
  )
  console.log(`${theme} url-stable:`, page.url().endsWith('/projects/7') ? 'PASS' : 'FAIL')
  await page.screenshot({ path: `${output}${theme}-3-after-drop.png` })

  await context.close()
}

await captureTheme('dark')
await captureTheme('light')
await browser.close()
console.log('artifacts written to', output)
