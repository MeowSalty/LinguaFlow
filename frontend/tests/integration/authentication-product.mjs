import assert from 'node:assert/strict'
import { createHash, randomBytes } from 'node:crypto'
import { spawn, execFileSync } from 'node:child_process'
import { createServer, request as httpRequest } from 'node:http'
import { createServer as createNetServer } from 'node:net'
import { mkdir, mkdtemp, writeFile, readFile, stat } from 'node:fs/promises'
import { createWriteStream } from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { deflateSync } from 'node:zlib'
import { chromium, expect } from '@playwright/test'

const frontend = fileURLToPath(new URL('../../', import.meta.url))
const backend = path.resolve(
  process.env.LINGUAFLOW_TEST_BACKEND ?? path.join(frontend, '../../LinguaFlow-backend'),
)
const artifacts = path.join(frontend, 'tests/artifacts/authentication-product')
await mkdir(artifacts, { recursive: true })
const root = await mkdtemp(path.join(artifacts, 'run-'))
const binaryBase = path.join(root, 'linguaflow')
const binary = binaryBase + (process.platform === 'win32' ? '.exe' : '')
const metadata = {
  backendCommit: execFileSync('git', ['rev-parse', 'HEAD'], {
    cwd: backend,
    encoding: 'utf8',
    windowsHide: true,
  }).trim(),
  testedAt: new Date().toISOString(),
  database: 'isolated SQLite',
  provider: 'controlled OAuth authorization server',
  results: [],
  limitations: [
    'GitHub and other hosted identity providers not tested',
    'PostgreSQL not exercised by this frontend suite',
  ],
}
const runTask = (args) =>
  process.platform === 'win32'
    ? execFileSync(
        'powershell.exe',
        [
          '-NoProfile',
          '-Command',
          'task backend:build "BACKEND_BINARY=$env:LF_AUTH_PRODUCT_BINARY"; exit $LASTEXITCODE',
        ],
        {
          cwd: backend,
          env: { ...process.env, LF_AUTH_PRODUCT_BINARY: binaryBase },
          stdio: 'inherit',
          windowsHide: true,
          timeout: 120_000,
        },
      )
    : execFileSync('task', args, { cwd: backend, stdio: 'inherit', timeout: 120_000 })
runTask(['backend:build', `BACKEND_BINARY=${binaryBase}`])
const contract = await readFile(path.join(frontend, '../api/openapi/openapi-3.0.yaml'), 'utf8')
const backendContract = await readFile(path.join(backend, 'api/openapi/openapi-3.0.yaml'), 'utf8')
assert.equal(
  contract.replaceAll('\r\n', '\n'),
  backendContract.replaceAll('\r\n', '\n'),
  'Frontend/backend contracts differ',
)
metadata.contractSha256 = createHash('sha256').update(contract).digest('hex')
const { controlledOAuth } = await import(
  pathToFileURL(path.join(backend, 'backend/internal/authacceptance/oauth2.mjs')).href
)
let browser
const children = []
const servers = []
function logoFixture() {
  function chunk(type, data) {
    const payload = Buffer.concat([Buffer.from(type), data])
    let crc = 0xffffffff
    for (const byte of payload) {
      crc ^= byte
      for (let bit = 0; bit < 8; bit++) crc = crc & 1 ? 0xedb88320 ^ (crc >>> 1) : crc >>> 1
    }
    const size = Buffer.alloc(4)
    size.writeUInt32BE(data.length)
    const checksum = Buffer.alloc(4)
    checksum.writeUInt32BE((crc ^ 0xffffffff) >>> 0)
    return Buffer.concat([size, payload, checksum])
  }
  const header = Buffer.alloc(13)
  header.writeUInt32BE(1, 0)
  header.writeUInt32BE(1, 4)
  header[8] = 8
  header[9] = 6
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    chunk('IHDR', header),
    chunk('IDAT', deflateSync(Buffer.from([0, 45, 110, 230, 255]))),
    chunk('IEND', Buffer.alloc(0)),
  ])
}
async function freePort() {
  const server = createNetServer()
  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const port = server.address().port
  await new Promise((resolve) => server.close(resolve))
  return port
}
async function ready(url, child) {
  for (let attempt = 0; attempt < 120; attempt++) {
    if (child.exitCode !== null) throw new Error('Test backend exited before readiness')
    try {
      if ((await fetch(url)).ok) return
    } catch {
      /* Wait for this owned process. */
    }
    await delay(250)
  }
  throw new Error('Test backend readiness timed out')
}
async function deploy(mode) {
  const directory = path.join(root, mode)
  await mkdir(directory)
  const apiPort = await freePort()
  const apiOrigin = `http://127.0.0.1:${apiPort}`
  const dist = path.join(frontend, 'dist')
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://localhost')
    if (url.pathname.startsWith('/api/')) {
      const upstream = httpRequest(
        {
          host: '127.0.0.1',
          port: apiPort,
          method: req.method,
          path: req.url,
          headers: req.headers,
        },
        (answer) => {
          res.writeHead(answer.statusCode, answer.headers)
          answer.pipe(res)
        },
      )
      upstream.on('error', () => {
        res.writeHead(502)
        res.end()
      })
      req.pipe(upstream)
      return
    }
    try {
      const requested = path.resolve(dist, '.' + decodeURIComponent(url.pathname))
      let file =
        requested.startsWith(dist + path.sep) && (await stat(requested).catch(() => null))?.isFile()
          ? requested
          : path.join(dist, 'index.html')
      const mime = {
        '.html': 'text/html',
        '.js': 'text/javascript',
        '.css': 'text/css',
        '.svg': 'image/svg+xml',
        '.ico': 'image/x-icon',
        '.woff2': 'font/woff2',
      }
      res.writeHead(200, {
        'Content-Type': mime[path.extname(file)] ?? 'application/octet-stream',
        'Cache-Control': 'no-store',
        'Referrer-Policy': 'no-referrer',
      })
      res.end(await readFile(file))
    } catch {
      res.writeHead(500)
      res.end()
    }
  })
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
  servers.push(server)
  const origin = `http://127.0.0.1:${server.address().port}`
  const publicBase = mode === 'same-origin' ? origin : apiOrigin
  const apiBase = publicBase + '/api/v1'
  const secrets = []
  const provider = await controlledOAuth({ callbackBases: [publicBase], secrets })
  servers.push(provider.server)
  const adminPassword = randomBytes(24).toString('base64url')
  const config = {
    kind: 'server',
    version: 1,
    server: {
      host: '127.0.0.1',
      port: apiPort,
      data_dir: directory,
      serve_ui: false,
      auto_migrate: true,
      jwt_secret: randomBytes(32).toString('base64url'),
      credentials: { master_key: randomBytes(32).toString('base64') },
      storage: { initialization: { capacity_bytes: 1073741824, logical_limit_bytes: 1073741824 } },
      authentication: {
        public_base_url: publicBase,
        completion_urls: [origin + '/auth/complete'],
        allowed_cidrs: ['127.0.0.1/32'],
        allow_http_loopback: true,
        rate_limit_per_minute: 1000,
      },
      cors: { allowed_origins: [origin] },
    },
    bootstrap: {
      registration_enabled: true,
      admin: { username: 'admin', password: adminPassword },
    },
  }
  const configFile = path.join(directory, 'server.json')
  await writeFile(configFile, JSON.stringify(config), { mode: 0o600 })
  const cleanEnv = Object.fromEntries(
    Object.entries(process.env).filter(([key]) => !key.startsWith('LINGUAFLOW_')),
  )
  const child = spawn(binary, ['serve', '--config', configFile], {
    cwd: directory,
    env: cleanEnv,
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  children.push(child)
  const log = createWriteStream(path.join(directory, 'backend.log'))
  child.stdout.pipe(log)
  child.stderr.pipe(log)
  await ready(apiOrigin + '/health/ready', child)
  return { origin, apiBase, provider, adminPassword }
}
async function api(page, base, endpoint, method = 'GET', body) {
  const token = await page.evaluate(() => localStorage.getItem('linguaflow.access_token'))
  const response = await fetch(base + endpoint, {
    method,
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    ...(body === undefined || method === 'GET' ? {} : { body: JSON.stringify(body) }),
  })
  assert.ok(response.ok, `${method} ${endpoint}: ${response.status}`)
  return response.status === 204 ? undefined : response.json()
}
async function login(page, app, username, password) {
  await page.goto(app.origin + '/login')
  await page.locator('input[autocomplete="username"]').fill(username)
  await page.locator('input[autocomplete="current-password"]').fill(password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(app.origin + '/')
}
async function authorize(page, provider, username) {
  await expect(page.locator('#username')).toBeVisible()
  await page.locator('#username').fill(username)
  await page.locator('#password').fill(provider.password)
  await page.locator('#oauth-approve').click()
}
async function scenario(mode) {
  const app = await deploy(mode)
  const contexts = []
  async function newPage() {
    const context = await browser.newContext()
    contexts.push(context)
    await context.addInitScript(
      (base) => localStorage.setItem('linguaflow.api_base_url', base),
      app.apiBase,
    )
    return context.newPage()
  }
  const admin = await newPage()
  const page = await newPage()
  const pageErrors = []
  for (const target of [admin, page])
    target.on('pageerror', (error) => pageErrors.push(error.message))
  await login(admin, app, 'admin', app.adminPassword)
  let connection = await api(admin, app.apiBase, '/admin/auth-connections', 'POST', {
    kind: 'generic_oauth2',
    display_name: 'Product OAuth',
    client_secret: app.provider.clientSecret,
    oauth2: {
      authorization_endpoint: app.provider.base + '/authorize',
      token_endpoint: app.provider.base + '/token',
      profile_endpoint: app.provider.base + '/user',
      client_id: 'b3-basic',
      client_auth_method: 'client_secret_basic',
      scopes: [],
      subject_mapping: { pointer: '/id', type: 'integer' },
    },
  })
  const connectionPath = '/admin/auth-connections/' + connection.id
  connection = await api(admin, app.apiBase, connectionPath, 'PATCH', {
    expected_revision: connection.aggregate_revision,
    policy: {
      enabled: true,
      login_enabled: true,
      signup_enabled: true,
      link_enabled: true,
      login_rules: { mode: 'unrestricted' },
      signup_rules: { mode: 'unrestricted' },
      link_rules: { mode: 'unrestricted' },
    },
  })
  await admin.goto(app.origin + '/admin/authentication')
  const row = admin.getByTestId('auth-connection-' + connection.id)
  await row.getByRole('button', { name: '编辑草稿', exact: true }).click()
  await admin
    .locator('input[type="file"]')
    .setInputFiles({ name: 'product.png', mimeType: 'image/png', buffer: logoFixture() })
  await expect(admin.getByText('图标已保存到草稿，请检查并发布。')).toBeVisible()
  await admin.getByRole('button', { name: '关闭', exact: true }).click()
  await row.getByRole('button', { name: '真实测试', exact: true }).click()
  await authorize(admin, app.provider, 'provideradmin')
  await expect(admin).toHaveURL(app.origin + '/admin/authentication')
  assert.equal((await api(admin, app.apiBase, '/users/me')).username, 'admin')
  connection = await api(admin, app.apiBase, connectionPath)
  await api(admin, app.apiBase, connectionPath + '/publish', 'POST', {
    expected_revision: connection.aggregate_revision,
  })
  await page.goto(app.origin + '/login')
  await page.getByRole('button', { name: '使用 Product OAuth 继续' }).click()
  await authorize(page, app.provider, 'alice')
  await expect(page).toHaveURL(app.origin + '/auth/complete')
  await expect(page.getByRole('heading', { name: '创建你的账号' })).toBeVisible()
  const password = randomBytes(16).toString('hex')
  await page.locator('input[autocomplete="username"]').fill(' Product_User ')
  await page.locator('input[autocomplete="new-password"]').nth(0).fill(password)
  await page.locator('input[autocomplete="new-password"]').nth(1).fill(password)
  await page.getByRole('button', { name: '创建账号并登录' }).click()
  await expect(page).toHaveURL(app.origin + '/')
  const user = await api(page, app.apiBase, '/users/me')
  assert.equal(user.username, 'product_user')
  assert.equal(user.email, null)
  await page.goto(app.origin + '/settings/security')
  const methods = await api(page, app.apiBase, '/users/me/login-methods')
  assert.equal(methods.password.is_set, true)
  assert.equal(methods.bindings[0].can_reauthenticate, false)
  await page.screenshot({ path: path.join(root, `${mode}-security.png`), fullPage: true })
  await page.getByText('已绑定的登录方式', { exact: true }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: path.join(root, `${mode}-login-methods.png`), fullPage: true })
  const newPassword = randomBytes(16).toString('hex')
  await page.locator('input[autocomplete="current-password"]').fill(password)
  await page.locator('input[autocomplete="new-password"]').nth(0).fill(newPassword)
  await page.locator('input[autocomplete="new-password"]').nth(1).fill(newPassword)
  await page.getByRole('button', { name: '修改密码', exact: true }).click()
  await expect(page).toHaveURL(/\/login/)
  assert.equal(await page.evaluate(() => localStorage.getItem('linguaflow.access_token')), null)
  await login(page, app, 'product_user', newPassword)
  assert.equal((await api(page, app.apiBase, '/users/me')).id, user.id)
  assert.equal(app.provider.errors.length, 0)
  assert.deepEqual(pageErrors, [])
  metadata.results.push({
    mode,
    status: 'passed',
    flows: [
      'password_login',
      'admin_real_oauth_test_preserves_identity',
      'logo_upload_and_publication',
      'explicit_oauth_signup',
      'initial_password',
      'nullable_email',
      'large_integer_provider_identity',
      'security_capabilities',
      'password_change_revocation_and_relogin',
      'callback_url_ticket_removal',
    ],
  })
  for (const context of contexts) await context.close()
  console.log(`PASS authentication product: ${mode}`)
}
try {
  browser = await chromium.launch({
    headless: true,
    ...(process.env.LINGUAFLOW_TEST_CHROMIUM
      ? { executablePath: process.env.LINGUAFLOW_TEST_CHROMIUM }
      : {}),
  })
  metadata.browserVersion = browser.version()
  await scenario('same-origin')
  await scenario('cross-origin')
  await writeFile(path.join(root, 'report.json'), JSON.stringify(metadata, null, 2))
  console.log(`Authentication product report: ${path.join(root, 'report.json')}`)
} catch (error) {
  await writeFile(
    path.join(root, 'report.json'),
    JSON.stringify({ ...metadata, status: 'failed', error: error.message }, null, 2),
  )
  console.error(`Authentication product failed: ${error.message}\nDiagnostics: ${root}`)
  process.exitCode = 1
} finally {
  await browser?.close()
  for (const child of children)
    if (child.exitCode === null && child.pid) {
      if (process.platform === 'win32')
        execFileSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], {
          windowsHide: true,
          stdio: 'ignore',
        })
      else child.kill('SIGTERM')
    }
  for (const server of servers) {
    server.closeAllConnections()
    await new Promise((resolve) => server.close(resolve))
  }
}
