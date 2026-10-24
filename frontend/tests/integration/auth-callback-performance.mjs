import assert from 'node:assert/strict'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { chromium } from '@playwright/test'
import { preview } from 'vite'

const frontend = new URL('../../', import.meta.url)
const evidence = new URL('test-results/auth-complete-improvement/', frontend)
await mkdir(evidence, { recursive: true })
const budget = JSON.parse(await readFile(new URL('bundle-budget.json', evidence), 'utf8'))
const session = {
  user: { id: 42, username: 'member', email: null, role: 'user', active: true },
  access_token: 'performance-access',
  refresh_token: 'performance-refresh',
  token_type: 'Bearer',
  expires_at: '2099-01-01T00:00:00Z',
  refresh_expires_at: '2099-02-01T00:00:00Z',
}
const server = await preview({
  root: fileURLToPath(frontend),
  preview: { host: '127.0.0.1', port: 4198, strictPort: true },
  plugins: [
    {
      name: 'callback-performance-fixtures',
      configurePreviewServer(server) {
        server.middlewares.use((request, response, next) => {
          const path = request.url?.split('?')[0]
          if (path === '/relay-observer') {
            response.setHeader('Content-Type', 'text/html')
            response.end('<!doctype html><title>Relay observer</title>')
            return
          }
          const fixtures = {
            '/api/v1/ping': { status: 'ok', service: 'Performance fixture' },
            '/api/v1/mode': { mode: 'server' },
            '/api/v1/auth/external/exchange': { kind: 'session', session },
          }
          if (!(path in fixtures)) return next()
          response.setHeader('Content-Type', 'application/json')
          response.setHeader('Cache-Control', 'no-store')
          response.end(JSON.stringify(fixtures[path]))
        })
      },
    },
  ],
})
const origin = 'http://127.0.0.1:4198'
const browser = await chromium.launch({ headless: true })
const measurements = []
try {
  for (const throttled of [false, true]) {
    for (const kind of ['relay', 'exchange']) {
      const context = await browser.newContext({ serviceWorkers: 'block' })
      const page = await context.newPage()
      let releaseResources
      const blocked = new Promise((resolve) => {
        releaseResources = resolve
      })
      try {
        const allowed = new Set(budget[kind].files.map((file) => `/${file}`))
        await page.route('**/assets/**', async (route) => {
          if (!allowed.has(new URL(route.request().url()).pathname)) await blocked
          await route.continue().catch(() => undefined)
        })
        const cdp = await context.newCDPSession(page)
        await cdp.send('Network.enable')
        await cdp.send('Network.setCacheDisabled', { cacheDisabled: true })
        if (throttled)
          await cdp.send('Network.emulateNetworkConditions', {
            offline: false,
            latency: 300,
            downloadThroughput: 16_384,
            uploadThroughput: 16_384,
            connectionType: 'cellular3g',
          })
        await page.addInitScript(
          ({ kind }) => {
            localStorage.setItem('linguaflow.api_base_url', '/api/v1')
            localStorage.setItem('linguaflow.auth_epoch', 'performance-epoch')
            localStorage.removeItem('linguaflow.access_token')
            localStorage.removeItem('linguaflow.refresh_token')
            if (kind === 'relay')
              sessionStorage.setItem(
                'linguaflow.connection-test-relay.v1',
                JSON.stringify({
                  channel: 'linguaflow-auth-test-performance',
                  transactionId: 'tx-performance',
                  expiresAt: new Date(Date.now() + 120_000).toISOString(),
                }),
              )
            else
              sessionStorage.setItem(
                'linguaflow.external-flow.v2',
                JSON.stringify({
                  baseUrl: new URL('/api/v1', location.origin).href,
                  epoch: 'performance-epoch',
                  userId: null,
                  purpose: 'login',
                  connectionId: 'company',
                  transactionId: 'tx-performance',
                  expiresAt: new Date(Date.now() + 300_000).toISOString(),
                  returnTo: '/settings/profile',
                  stage: 'started',
                  verifier: 'v'.repeat(43),
                }),
              )
          },
          { kind },
        )
        let relayReceived
        const relay = new Promise((resolve) => {
          relayReceived = resolve
        })
        if (kind === 'relay') {
          const monitor = await context.newPage()
          await monitor.goto(`${origin}/relay-observer`)
          await monitor.exposeBinding('recordRelay', (_source, valid) => {
            assert.equal(valid, true)
            relayReceived(Date.now())
          })
          await monitor.evaluate(() => {
            const channel = new BroadcastChannel('linguaflow-auth-test-performance')
            channel.onmessage = (event) => {
              void window.recordRelay(
                event.origin === location.origin &&
                  event.data?.type === 'ticket' &&
                  event.data?.transactionId === 'tx-performance' &&
                  event.data?.ticket === 'performance-ticket',
              )
            }
          })
        }
        const started = Date.now()
        await page.goto(`${origin}/auth/complete?ticket=performance-ticket`, {
          waitUntil: 'commit',
        })
        let completed
        if (kind === 'relay')
          completed = await Promise.race([
            relay,
            new Promise((_, reject) => {
              const timer = setTimeout(() => reject(new Error('Relay deadline exceeded')), 20_000)
              timer.unref()
            }),
          ])
        else {
          await page.waitForFunction(
            () =>
              document.getElementById('auth-startup-message')?.textContent?.includes('认证已验证'),
            undefined,
            { timeout: 20_000 },
          )
          completed = Date.now()
        }
        await page.evaluate(
          () =>
            new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
        )
        const timing = await page.evaluate(() => ({
          paints: performance
            .getEntriesByType('paint')
            .map((item) => ({ name: item.name, milliseconds: item.startTime })),
          htmlResponseEnd: performance.getEntriesByType('navigation')[0].responseEnd,
          scripts: performance
            .getEntriesByType('resource')
            .filter((item) => /\/assets\/.+\.js$/.test(item.name))
            .map((item) => ({
              file: new URL(item.name).pathname,
              milliseconds: item.responseEnd,
              bytes: item.decodedBodySize,
            })),
          queryCleared: !location.search,
          ticketCleared: !('__linguaflowAuthTicket' in window),
          flowCleared: sessionStorage.getItem('linguaflow.external-flow.v2') === null,
        }))
        assert.ok(timing.queryCleared && timing.ticketCleared && timing.flowCleared)
        assert.ok(
          timing.scripts.every((script) => allowed.has(script.file)),
          'Application script completed before handoff',
        )
        await page.screenshot({
          path: fileURLToPath(
            new URL(`performance-${kind}-${throttled ? '128kbps' : 'normal'}.png`, evidence),
          ),
        })
        const measurement = {
          kind,
          network: throttled ? { kbps: 128, latencyMs: 300 } : 'unthrottled-local',
          completionMilliseconds: completed - started,
          ...timing,
        }
        measurements.push(measurement)
        console.log(
          `${kind} / ${throttled ? '128kbps + 300ms' : 'local'}: ${measurement.completionMilliseconds}ms; FCP ${timing.paints.find((paint) => paint.name === 'first-contentful-paint')?.milliseconds}ms`,
        )
      } finally {
        releaseResources()
        await context.close()
      }
    }
  }
} finally {
  await browser.close()
  await new Promise((resolve) => server.httpServer.close(resolve))
}
await writeFile(
  new URL('performance-summary.json', evidence),
  `${JSON.stringify({ scope: 'Local production preview; real HTTP fixtures; cold browser caches; business resources blocked; not a public deployment test', budget, measurements }, null, 2)}\n`,
)
