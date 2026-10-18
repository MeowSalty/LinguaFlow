import { randomBytes } from 'node:crypto'
import { createServer, request as httpRequest } from 'node:http'
import {
  access,
  appendFile,
  cp,
  mkdir,
  readFile,
  readdir,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises'
import { createWriteStream } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { createServer as createNetServer } from 'node:net'

const sourceDir = path.dirname(fileURLToPath(import.meta.url))
const frontendDir = path.resolve(sourceDir, '../..')
const backendDir = path.resolve(
  process.env.LINGUAFLOW_TEST_BACKEND ?? path.join(frontendDir, '../../LinguaFlow-backend'),
)
const artifactsDir = path.join(frontendDir, 'tests/artifacts/authentication-product')

const secret = () => randomBytes(32).toString('base64url')
const realmName = `linguaflow-b4-${process.pid}-${randomBytes(5).toString('hex')}`

export const b4ArtifactsDir = artifactsDir
export const b4BackendDir = backendDir

async function exists(file) {
  try {
    await access(file)
    return true
  } catch {
    return false
  }
}

export async function run(program, args, options = {}) {
  if (program === 'task' && process.platform === 'win32') {
    const env = { ...(options.env ?? process.env), LF_B4_TASK_ARGS: JSON.stringify(args) }
    return run(
      'powershell.exe',
      [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        '$taskArgs = ConvertFrom-Json $env:LF_B4_TASK_ARGS; & task @taskArgs; exit $LASTEXITCODE',
      ],
      { ...options, env },
    )
  }
  return new Promise((resolve, reject) => {
    const child = spawn(program, args, {
      windowsHide: true,
      stdio: 'inherit',
      ...options,
    })
    child.once('error', reject)
    child.once('exit', (code) => {
      if (code === 0) resolve()
      else reject(new Error(`${program} exited with ${code ?? 'unknown status'}`))
    })
  })
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

async function listen(server, port) {
  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(port, '127.0.0.1', resolve)
  })
  return server
}

async function ready(url, child, timeout = 120_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (child?.exitCode !== null && child?.exitCode !== undefined)
      throw new Error('B4 owned process exited before readiness')
    try {
      if ((await fetch(url, { signal: AbortSignal.timeout(1500) })).ok) return
    } catch {
      /* The owned process is still starting. */
    }
    await new Promise((resolve) => setTimeout(resolve, 300))
  }
  throw new Error(`B4 readiness timed out: ${new URL(url).pathname}`)
}

function cleanEnvironment() {
  return Object.fromEntries(
    Object.entries(process.env).filter(
      ([key]) => !key.startsWith('LINGUAFLOW_') && !key.startsWith('KC_'),
    ),
  )
}

function safeName(value) {
  return value.replace(/[^a-zA-Z0-9._-]/g, '_')
}

async function noteProgress(stage) {
  const line = `${new Date().toISOString()} ${stage}`
  await appendFile(path.join(artifactsDir, 'b4-runtime-readiness.log'), `${line}\n`, 'utf8').catch(
    () => {},
  )
}

function managedB4Root(input) {
  const resolved = path.resolve(input)
  const relative = path.relative(path.resolve(artifactsDir), resolved)
  if (
    !relative ||
    relative === '..' ||
    relative.startsWith(`..${path.sep}`) ||
    path.isAbsolute(relative) ||
    !path.basename(resolved).startsWith('b4-run-')
  ) {
    throw new Error(`Refusing to remove unmanaged B4 runtime directory: ${relative || '<none>'}`)
  }
  return resolved
}

async function findPostgresBin() {
  const explicit = [
    process.env.LINGUAFLOW_AUTH_POSTGRES_BIN,
    process.env.LINGUAFLOW_TEST_POSTGRES_BIN,
  ].filter(Boolean)
  const known = [
    path.join(
      process.env.TEMP ?? tmpdir(),
      'codex-linguaflow-pg16-42ef1d0142fa461fbe01a46eb17220f8',
      'pgsql',
      'bin',
    ),
    path.join(tmpdir(), 'linguaflow-postgres', 'bin'),
    'C:\\Program Files\\PostgreSQL\\16\\bin',
    'C:\\Program Files\\PostgreSQL\\17\\bin',
    'C:\\Program Files\\PostgreSQL\\18\\bin',
  ]
  const candidates = [...explicit, ...known]
  for (const candidate of candidates) {
    const bin = path.resolve(candidate)
    const names =
      process.platform === 'win32'
        ? ['postgres.exe', 'initdb.exe', 'pg_isready.exe', 'createdb.exe']
        : ['postgres', 'initdb', 'pg_isready', 'createdb']
    if (
      await Promise.all(names.map((name) => exists(path.join(bin, name)))).then((items) =>
        items.every(Boolean),
      )
    )
      return bin
  }
  return null
}

async function buildBackend(binaryBase) {
  const env = { ...process.env, LF_B4_BINARY: binaryBase }
  if (process.platform === 'win32') {
    await run(
      'powershell.exe',
      [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        '& task backend:build "BACKEND_BINARY=$env:LF_B4_BINARY"; exit $LASTEXITCODE',
      ],
      { cwd: backendDir, env },
    )
  } else {
    await run('task', ['backend:build', `BACKEND_BINARY=${binaryBase}`], {
      cwd: backendDir,
      env,
    })
  }
}

async function proxy(req, res, port, incomingPath, stripPrefix) {
  const upstream = httpRequest(
    {
      host: '127.0.0.1',
      port,
      method: req.method,
      path: incomingPath.slice(stripPrefix.length) || '/',
      headers: { ...req.headers, host: '127.0.0.1' },
    },
    (answer) => {
      res.writeHead(answer.statusCode ?? 502, answer.headers)
      answer.pipe(res)
    },
  )
  upstream.once('error', () => {
    if (!res.headersSent) res.writeHead(502)
    res.end()
  })
  req.pipe(upstream)
}

async function serveDist(dist, req, res) {
  try {
    const requestURL = new URL(req.url, 'http://127.0.0.1')
    const decoded = decodeURIComponent(requestURL.pathname)
    const requested = path.resolve(dist, `.${decoded}`)
    const inside = requested === dist || requested.startsWith(`${dist}${path.sep}`)
    const file = inside && (await exists(requested)) ? requested : path.join(dist, 'index.html')
    const mime = {
      '.css': 'text/css; charset=utf-8',
      '.html': 'text/html; charset=utf-8',
      '.ico': 'image/x-icon',
      '.js': 'text/javascript; charset=utf-8',
      '.json': 'application/json; charset=utf-8',
      '.png': 'image/png',
      '.svg': 'image/svg+xml',
      '.woff2': 'font/woff2',
    }
    res.writeHead(200, {
      'Cache-Control': 'no-store',
      'Content-Type': mime[path.extname(file)] ?? 'application/octet-stream',
      'Referrer-Policy': 'no-referrer',
    })
    res.end(await readFile(file))
  } catch {
    if (!res.headersSent) res.writeHead(500)
    res.end()
  }
}

async function startKeycloak(runtime, callbackBases) {
  const { keycloakHome, prepareKeycloak } = await import(
    pathToFileURL(path.join(backendDir, 'backend/internal/authacceptance/prepare.mjs')).href
  )
  await prepareKeycloak()
  const home = path.join(runtime.root, 'keycloak')
  await cp(keycloakHome, home, { recursive: true })
  const port = await freePort()
  const managementPort = await freePort()
  const clientID = 'b4-oidc'
  const clientSecret = secret()
  const password = secret()
  runtime.secrets.push(clientSecret, password)
  const callbackPaths = callbackBases.map((base) => `${base}/api/v1/auth/external/*`)
  const realm = {
    realm: realmName,
    enabled: true,
    sslRequired: 'none',
    registrationAllowed: false,
    clients: [
      {
        clientId: clientID,
        enabled: true,
        protocol: 'openid-connect',
        publicClient: false,
        secret: clientSecret,
        standardFlowEnabled: true,
        directAccessGrantsEnabled: false,
        redirectUris: callbackPaths,
        attributes: { 'pkce.code.challenge.method': 'S256' },
        defaultClientScopes: ['basic', 'profile', 'email'],
      },
    ],
    users: ['provideradmin'].map((username) => ({
      username,
      enabled: true,
      email: `${username}@example.invalid`,
      emailVerified: true,
      firstName: username,
      lastName: 'Acceptance',
      credentials: [{ type: 'password', value: password, temporary: false }],
    })),
  }
  await mkdir(path.join(home, 'data/import'), { recursive: true })
  await writeFile(path.join(home, 'data/import/realm.json'), JSON.stringify(realm), { mode: 0o600 })
  const log = createWriteStream(path.join(runtime.root, 'keycloak.log'), { flags: 'a' })
  const env = {
    ...cleanEnvironment(),
    LF_B4_KC: path.join(home, 'bin', 'kc.bat'),
    LF_B4_KC_PORT: String(port),
    LF_B4_KC_MANAGEMENT_PORT: String(managementPort),
  }
  const child =
    process.platform === 'win32'
      ? spawn(
          'powershell.exe',
          [
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            '& $env:LF_B4_KC start-dev --http-host=127.0.0.1 --http-port=$env:LF_B4_KC_PORT --import-realm --hostname-strict=false --http-management-port=$env:LF_B4_KC_MANAGEMENT_PORT',
          ],
          { cwd: runtime.root, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] },
        )
      : spawn(
          path.join(home, 'bin', 'kc.sh'),
          [
            'start-dev',
            '--http-host=127.0.0.1',
            `--http-port=${port}`,
            '--import-realm',
            '--hostname-strict=false',
            `--http-management-port=${managementPort}`,
          ],
          { cwd: runtime.root, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] },
        )
  child.stdout.pipe(log)
  child.stderr.pipe(log)
  runtime.children.push(child)
  const issuer = `http://127.0.0.1:${port}/realms/${realmName}`
  await ready(`${issuer}/.well-known/openid-configuration`, child, 180_000)
  return { issuer, clientID, clientSecret, password }
}

export async function createB4Runtime() {
  await mkdir(artifactsDir, { recursive: true })
  const root = await import('node:fs/promises').then(({ mkdtemp }) =>
    mkdtemp(path.join(artifactsDir, 'b4-run-')),
  )
  const runtime = { root, children: [], servers: [], secrets: [] }
  const deployments = new Map()
  const userOIDCByDeployment = new Map()
  let closed = false
  let postgresOwner
  let keycloak
  let oauth
  const binaryBase = path.join(root, 'artifacts', 'linguaflow')
  const binary = `${binaryBase}${process.platform === 'win32' ? '.exe' : ''}`
  await mkdir(path.dirname(binary), { recursive: true })
  const ports = {
    sqliteUI: await freePort(),
    postgresUI: await freePort(),
    postgresGateway: await freePort(),
  }
  const sqliteUIOrigin = `http://127.0.0.1:${ports.sqliteUI}`
  const postgresUIOrigin = `http://127.0.0.1:${ports.postgresUI}`
  const postgresGatewayOrigin = `http://127.0.0.1:${ports.postgresGateway}`
  const sqlitePublicBase = sqliteUIOrigin
  const postgresPublicBase = `${postgresGatewayOrigin}/gateway`
  const postgresBin = await findPostgresBin()
  if (postgresBin) process.env.LINGUAFLOW_AUTH_POSTGRES_BIN = postgresBin
  try {
    await buildBackend(binaryBase)
    await noteProgress('backend_build=passed')
    const backendAcceptance = await import(
      pathToFileURL(path.join(backendDir, 'backend/internal/authacceptance/runtime.mjs')).href
    )
    postgresOwner = await backendAcceptance.createRuntime()
    await noteProgress('postgres_runtime=created')
    keycloak = await startKeycloak(runtime, [sqlitePublicBase, postgresPublicBase])
    await noteProgress('keycloak_readiness=passed')
    const { controlledOAuth } = await import(
      pathToFileURL(path.join(backendDir, 'backend/internal/authacceptance/oauth2.mjs')).href
    )
    oauth = await controlledOAuth({
      callbackBases: [sqlitePublicBase, postgresPublicBase],
      secrets: runtime.secrets,
    })
    runtime.servers.push(oauth.server)
    await noteProgress('controlled_oauth=ready')
  } catch (error) {
    await close()
    throw error
  }

  async function startDeployment(kind) {
    const cached = deployments.get(kind)
    if (cached) return cached
    const postgres = kind === 'postgresql' ? await postgresOwner.postgres() : null
    await noteProgress(`${kind}_database=ready`)
    const crossOrigin = kind === 'postgresql'
    const uiOrigin = crossOrigin ? postgresUIOrigin : sqliteUIOrigin
    const publicBase = crossOrigin ? postgresPublicBase : sqlitePublicBase
    const apiBase = `${publicBase}/api/v1`
    const directory = path.join(root, safeName(kind))
    await mkdir(directory, { recursive: true })
    const adminPassword = secret()
    runtime.secrets.push(adminPassword)
    const config = {
      kind: 'server',
      version: 1,
      server: {
        host: '127.0.0.1',
        port: await freePort(),
        data_dir: directory,
        serve_ui: false,
        auto_migrate: true,
        jwt_secret: secret(),
        credentials: { master_key: randomBytes(32).toString('base64') },
        storage: {
          initialization: { capacity_bytes: 1073741824, logical_limit_bytes: 1073741824 },
        },
        ...(postgres ? { database: { driver: 'postgres', dsn: postgres.applicationDSN } } : {}),
        authentication: {
          public_base_url: publicBase,
          completion_urls: [`${uiOrigin}/auth/complete`],
          allowed_cidrs: ['127.0.0.1/32'],
          allow_http_loopback: true,
          rate_limit_per_minute: 1000,
        },
        cors: { allowed_origins: crossOrigin ? [uiOrigin] : [] },
      },
      bootstrap: {
        registration_enabled: true,
        admin: { username: 'admin', password: adminPassword },
      },
    }
    runtime.secrets.push(config.server.jwt_secret, config.server.credentials.master_key)
    const configPath = path.join(directory, 'server.json')
    await writeFile(configPath, JSON.stringify(config), { mode: 0o600 })
    const log = createWriteStream(path.join(directory, 'backend.log'), { flags: 'a' })
    const child = spawn(binary, ['serve', '--config', configPath], {
      cwd: directory,
      env: cleanEnvironment(),
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    child.stdout.pipe(log)
    child.stderr.pipe(log)
    runtime.children.push(child)
    await ready(`http://127.0.0.1:${config.server.port}/health/ready`, child)
    await noteProgress(`${kind}_backend_readiness=passed`)
    const dist = path.join(frontendDir, 'dist')
    const uiServer = await listen(
      createServer((req, res) => {
        if (!crossOrigin && req.url.startsWith('/api/')) {
          void proxy(req, res, config.server.port, req.url, '')
          return
        }
        void serveDist(dist, req, res)
      }),
      crossOrigin ? ports.postgresUI : ports.sqliteUI,
    )
    runtime.servers.push(uiServer)
    let gatewayServer
    if (crossOrigin) {
      gatewayServer = await listen(
        createServer((req, res) => {
          if (req.url.startsWith('/gateway/api/')) {
            void proxy(req, res, config.server.port, req.url, '/gateway')
            return
          }
          res.writeHead(404)
          res.end()
        }),
        ports.postgresGateway,
      )
      runtime.servers.push(gatewayServer)
    }
    const deployment = {
      kind,
      origin: uiOrigin,
      apiBase,
      publicBase,
      adminPassword,
      directory,
      browserConfig: { provider: keycloak, oauth },
    }
    deployments.set(kind, deployment)
    await noteProgress(`${kind}_deployment=ready`)
    return deployment
  }

  async function close() {
    if (closed) return
    closed = true
    const managedRoot = managedB4Root(root)
    const preservedDir = path.join(artifactsDir, `${safeName(path.basename(managedRoot))}-logs`)
    const copied = []
    try {
      await mkdir(preservedDir, { recursive: true })
      const entries = await readdir(managedRoot, { recursive: true, withFileTypes: true })
      for (const entry of entries) {
        if (!entry.isFile()) continue
        const source = path.join(entry.parentPath ?? entry.path, entry.name)
        if (!entry.name.endsWith('.log')) continue
        const relative = path.relative(managedRoot, source)
        const target = path.join(preservedDir, relative)
        await mkdir(path.dirname(target), { recursive: true })
        const text = await readFile(source, 'utf8').catch(() => '')
        let sanitized = text
        for (const secret of runtime.secrets)
          if (secret) sanitized = sanitized.replaceAll(secret, '[redacted]')
        await writeFile(target, sanitized, 'utf8')
        copied.push(relative)
      }
    } catch (error) {
      await noteProgress(`runtime_log_preservation=failed:${error.message}`).catch(() => {})
    }
    await postgresOwner?.close().catch(() => {})
    for (const child of runtime.children.reverse()) {
      if (child.exitCode !== null || !child.pid) continue
      if (process.platform === 'win32')
        await run('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' }).catch(
          () => {},
        )
      else child.kill('SIGTERM')
    }
    for (const server of runtime.servers) {
      server.closeAllConnections?.()
      await new Promise((resolve) => server.close(resolve)).catch(() => {})
    }
    try {
      const size = await stat(managedRoot)
        .then((value) => value.size)
        .catch(() => 0)
      await rm(managedRoot, { recursive: true, force: true })
      await noteProgress(`runtime_cleanup=passed:logs=${copied.length}`)
      await writeFile(
        path.join(artifactsDir, `${safeName(path.basename(managedRoot))}-cleanup.json`),
        JSON.stringify(
          { sanitized_logs: copied, source_file_count: size ? 'unknown' : 0 },
          null,
          2,
        ),
        'utf8',
      )
    } catch (error) {
      await noteProgress(`runtime_cleanup=failed:${error.message}`).catch(() => {})
      await writeFile(
        path.join(artifactsDir, `${safeName(path.basename(managedRoot))}-cleanup-error.txt`),
        error.message,
        'utf8',
      )
    }
  }

  return {
    root,
    binary,
    postgresBin,
    keycloak,
    oauth,
    sqlite: { origin: sqliteUIOrigin, publicBase: sqlitePublicBase },
    postgresql: { origin: postgresUIOrigin, publicBase: postgresPublicBase },
    startDeployment,
    close,
    userOIDCByDeployment,
    secrets: runtime.secrets,
  }
}
