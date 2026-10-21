import { createHash, randomBytes } from 'node:crypto'
import { createServer, request as httpRequest } from 'node:http'
import { createServer as createNetServer } from 'node:net'
import { execFileSync, spawn, spawnSync } from 'node:child_process'
import { createWriteStream } from 'node:fs'
import { access, cp, mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { once } from 'node:events'
import { createB5OAuth } from './authentication-b5-oauth.mjs'

const sourceDir = path.dirname(fileURLToPath(import.meta.url))
const frontendDir = path.resolve(sourceDir, '../..')
const backendDir = path.resolve(
  process.env.LINGUAFLOW_TEST_BACKEND ?? path.join(frontendDir, '../../LinguaFlow-backend'),
)
const artifactsDir = path.join(frontendDir, 'tests/artifacts/authentication-b5-product')
const backendRevision = '2bd3da822a2b10df7e67fab48ce81ba7254dd1e1'
const secret = () => randomBytes(32).toString('base64url')
const cellNames = ['sqlite-oidc', 'sqlite-oauth', 'postgresql-oidc', 'postgresql-oauth']

export const b5ArtifactsDir = artifactsDir
export const b5BackendDir = backendDir

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
    const env = { ...(options.env ?? process.env), LF_B5_TASK_ARGS: JSON.stringify(args) }
    return run(
      'powershell.exe',
      [
        '-NoProfile',
        '-NonInteractive',
        '-Command',
        '$taskArgs = ConvertFrom-Json $env:LF_B5_TASK_ARGS; & task @taskArgs; exit $LASTEXITCODE',
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

export async function createB5RunDirectory() {
  await mkdir(artifactsDir, { recursive: true })
  return mkdtemp(path.join(artifactsDir, 'run-'))
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

async function ready(url, child, label, timeout = 120_000) {
  const deadline = Date.now() + timeout
  while (Date.now() < deadline) {
    if (child?.exitCode !== null && child?.exitCode !== undefined)
      throw new Error(`${label} exited before readiness`)
    try {
      if ((await fetch(url, { signal: AbortSignal.timeout(1500) })).ok) return
    } catch {
      // Keep polling while the owned process starts.
    }
    await new Promise((resolve) => setTimeout(resolve, 300))
  }
  throw new Error(`${label} readiness timed out at ${new URL(url).pathname}`)
}

function cleanEnvironment(extra = {}) {
  return Object.fromEntries(
    Object.entries({ ...process.env, ...extra }).filter(
      ([key]) => !key.startsWith('LINGUAFLOW_') && !key.startsWith('KC_'),
    ),
  )
}

function safeName(value) {
  return value.replace(/[^a-zA-Z0-9._-]/gu, '_')
}

function inside(parent, child) {
  const relative = path.relative(path.resolve(parent), path.resolve(child))
  return (
    Boolean(relative) &&
    relative !== '..' &&
    !relative.startsWith(`..${path.sep}`) &&
    !path.isAbsolute(relative)
  )
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
  const names =
    process.platform === 'win32'
      ? ['postgres.exe', 'initdb.exe', 'pg_isready.exe', 'createdb.exe']
      : ['postgres', 'initdb', 'pg_isready', 'createdb']
  for (const candidate of [...explicit, ...known]) {
    const bin = path.resolve(candidate)
    if (
      await Promise.all(names.map((name) => exists(path.join(bin, name)))).then((xs) =>
        xs.every(Boolean),
      )
    )
      return bin
  }
  return null
}

async function javaHomeCandidates() {
  const homes = new Set(
    [
      process.env.LINGUAFLOW_B5_JAVA_HOME,
      process.env.JAVA_HOME,
      'C:/Program Files/Microsoft',
      'C:/Program Files/Eclipse Adoptium',
      'C:/Program Files/Amazon Corretto',
      'C:/Program Files/Java',
      path.join(process.env.USERPROFILE ?? '', '.jdks'),
      path.join(process.env.USERPROFILE ?? '', '.cache/codex-runtimes'),
      path.join(tmpdir(), 'linguaflow-jdks'),
    ].filter(Boolean),
  )
  const candidates = new Set()
  for (const home of homes) {
    if (path.basename(home).toLowerCase() === 'bin') candidates.add(path.dirname(home))
    else candidates.add(home)
    try {
      const entries = await readdir(home, { withFileTypes: true })
      for (const entry of entries) {
        if (
          entry.isDirectory() &&
          /(?:jdk|java).*(?:21|22|23|24|25)|(?:21|22|23|24|25).*(?:jdk|java)/iu.test(entry.name)
        )
          candidates.add(path.join(home, entry.name))
        if (entry.isDirectory() && /codex-primary-runtime/iu.test(entry.name)) {
          const nested = path.join(home, entry.name, 'dependencies')
          const children = await readdir(nested, { withFileTypes: true }).catch(() => [])
          for (const child of children)
            if (child.isDirectory()) candidates.add(path.join(nested, child.name))
        }
      }
    } catch {
      // Missing discovery roots are normal on other platforms.
    }
  }
  for (const bin of (process.env.PATH ?? '').split(path.delimiter)) {
    if (/java(?:\.exe)?$/iu.test(path.basename(bin))) candidates.add(path.dirname(bin))
    else if (await exists(path.join(bin, process.platform === 'win32' ? 'java.exe' : 'java')))
      candidates.add(path.dirname(bin))
  }
  return [...candidates]
}

async function findJava21() {
  const found = []
  for (const candidate of await javaHomeCandidates()) {
    const executable = path.join(
      candidate,
      'bin',
      process.platform === 'win32' ? 'java.exe' : 'java',
    )
    if (!(await exists(executable))) continue
    const result = spawnSync(executable, ['-version'], {
      encoding: 'utf8',
      windowsHide: true,
      timeout: 5000,
    })
    const versionText = `${result.stdout ?? ''}\n${result.stderr ?? ''}`
    const match = versionText.match(/(?:version\s+"|openjdk\s+)(\d+)/iu)
    const major = match ? Number(match[1]) : null
    found.push({ home: candidate, major })
    if (result.status === 0 && major >= 21) return { home: candidate, executable, major, found }
  }
  throw new Error(
    `Keycloak 26.8.0 requires Java 21 or newer; no compatible cached runtime was found. ` +
      `Discovered: ${found.map((item) => `${item.home} (Java ${item.major ?? 'unknown'})`).join(', ') || 'none'}. ` +
      'Set LINGUAFLOW_B5_JAVA_HOME to an existing portable JDK 21+ directory.',
  )
}

async function buildBackend(binaryBase, goPath) {
  const pathValue = [goPath, process.env.PATH].filter(Boolean).join(path.delimiter)
  const env = { ...process.env, PATH: pathValue, LF_B5_BINARY: binaryBase }
  await run('task', ['backend:build', `BACKEND_BINARY=${binaryBase}`], { cwd: backendDir, env })
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
    const resolvedDist = path.resolve(dist)
    const requested = path.resolve(resolvedDist, `.${decoded}`)
    const file =
      inside(resolvedDist, requested) && (await exists(requested))
        ? requested
        : path.join(resolvedDist, 'index.html')
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

async function startKeycloak(runtime, callbackBases, identitiesByCell, java) {
  const runtimeCache =
    process.env.LINGUAFLOW_AUTH_RUNTIME_DIR ?? path.join(tmpdir(), 'linguaflow-auth-b1-runtime')
  const oldCache = process.env.LINGUAFLOW_AUTH_RUNTIME_DIR
  process.env.LINGUAFLOW_AUTH_RUNTIME_DIR = runtimeCache
  let acceptance
  try {
    acceptance = await import(
      pathToFileURL(path.join(backendDir, 'backend/internal/authacceptance/prepare.mjs')).href
    )
    await acceptance.prepareKeycloak()
  } finally {
    if (oldCache === undefined) delete process.env.LINGUAFLOW_AUTH_RUNTIME_DIR
    else process.env.LINGUAFLOW_AUTH_RUNTIME_DIR = oldCache
  }

  const home = path.join(runtime.root, 'keycloak')
  await cp(acceptance.keycloakHome, home, { recursive: true })
  const port = await freePort()
  const managementPort = await freePort()
  const clientID = 'b5-oidc'
  const clientSecret = secret()
  const password = secret()
  runtime.secrets.push(clientSecret, password)
  const realmName = `linguaflow-b5-${process.pid}-${randomBytes(5).toString('hex')}`
  const callbackPaths = callbackBases.map((base) => `${base}/api/v1/auth/external/*`)
  const usernames = [...new Set(Object.values(identitiesByCell).flat())]
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
    users: usernames.map((username) => ({
      username,
      enabled: true,
      email: `${username}@example.invalid`,
      emailVerified: true,
      firstName: username,
      lastName: 'B5 Acceptance',
      credentials: [{ type: 'password', value: password, temporary: false }],
    })),
  }
  await mkdir(path.join(home, 'data/import'), { recursive: true })
  await writeFile(path.join(home, 'data/import/realm.json'), JSON.stringify(realm), { mode: 0o600 })
  const log = createWriteStream(path.join(runtime.root, 'keycloak.log'), {
    flags: 'a',
    mode: 0o600,
  })
  runtime.logs.push({
    name: 'keycloak.log',
    stream: log,
    source: path.join(runtime.root, 'keycloak.log'),
  })
  const env = cleanEnvironment({
    JAVA_HOME: java.home,
    PATH: [path.join(java.home, 'bin'), process.env.PATH].filter(Boolean).join(path.delimiter),
    LF_B5_KC: path.join(home, 'bin', 'kc.bat'),
    LF_B5_KC_PORT: String(port),
    LF_B5_KC_MANAGEMENT_PORT: String(managementPort),
  })
  const child =
    process.platform === 'win32'
      ? spawn(
          'powershell.exe',
          [
            '-NoProfile',
            '-NonInteractive',
            '-Command',
            '& $env:LF_B5_KC start-dev --http-host=127.0.0.1 --http-port=$env:LF_B5_KC_PORT --import-realm --hostname-strict=false --http-management-port=$env:LF_B5_KC_MANAGEMENT_PORT',
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
  await ready(`${issuer}/.well-known/openid-configuration`, child, 'Keycloak', 180_000)
  return { issuer, clientID, clientSecret, password, identitiesByCell, realm: realmName }
}

function cellIdentities(cellName) {
  const prefix = `b5_${cellName.replaceAll('-', '_')}`
  return {
    admin: `${prefix}_admin_oidc`,
    member: `${prefix}_member_oidc`,
    required: `${prefix}_required`,
    optionalSkip: `${prefix}_optional_skip`,
    optionalSet: `${prefix}_optional_set`,
    restore: `${prefix}_restore_admin`,
    link: `${prefix}_link_member`,
  }
}

export async function createB5Runtime({ runDir }) {
  const resolvedRunDir = path.resolve(runDir)
  if (!inside(artifactsDir, resolvedRunDir) || !path.basename(resolvedRunDir).startsWith('run-'))
    throw new Error(`Refusing unmanaged B5 run directory: ${resolvedRunDir}`)
  const dist = path.join(frontendDir, 'dist/index.html')
  await access(dist).catch(() => {
    throw new Error('frontend/dist is missing; run the B5 production build first')
  })

  const root = path.join(resolvedRunDir, 'runtime')
  await mkdir(root, { recursive: false })
  const runtime = {
    root,
    runDir: resolvedRunDir,
    children: [],
    servers: [],
    owners: [],
    logs: [],
    secrets: [],
  }
  const progressPath = path.join(resolvedRunDir, 'runtime-progress.log')
  const note = async (line) => {
    await writeFile(progressPath, `${new Date().toISOString()} ${line}\n`, {
      flag: 'a',
      encoding: 'utf8',
    })
  }
  let closed = false
  const binaryBase = path.join(root, 'artifacts', 'linguaflow')
  const binary = `${binaryBase}${process.platform === 'win32' ? '.exe' : ''}`
  const goPath = path.dirname(process.env.LINGUAFLOW_B5_GO ?? 'C:/Program Files/Go/bin/go.exe')
  const postgresBin = await findPostgresBin()
  if (!postgresBin)
    throw new Error('Portable PostgreSQL 16 binaries were not found in the configured B5 cache')

  const identities = Object.fromEntries(cellNames.map((name) => [name, cellIdentities(name)]))
  const ports = Object.fromEntries(
    await Promise.all(
      cellNames.map(async (name) => [
        name,
        {
          ui: await freePort(),
          api: await freePort(),
          gateway: name.startsWith('postgresql') ? await freePort() : null,
        },
      ]),
    ),
  )
  const origins = Object.fromEntries(
    cellNames.map((name) => {
      const uiOrigin = `http://127.0.0.1:${ports[name].ui}`
      const gatewayOrigin = ports[name].gateway ? `http://127.0.0.1:${ports[name].gateway}` : null
      const publicBase = gatewayOrigin ? `${gatewayOrigin}/gateway` : uiOrigin
      return [name, { uiOrigin, gatewayOrigin, publicBase, apiBase: `${publicBase}/api/v1` }]
    }),
  )

  try {
    const frozenDist = path.join(root, 'frontend-dist')
    await cp(path.join(frontendDir, 'dist'), frozenDist, {
      recursive: true,
      force: false,
      errorOnExist: true,
    })
    const buildFiles = []
    const collectBuildFiles = async (directory) => {
      for (const entry of await readdir(directory, { withFileTypes: true })) {
        const filename = path.join(directory, entry.name)
        if (entry.isDirectory()) await collectBuildFiles(filename)
        else if (entry.isFile())
          buildFiles.push({
            path: path.relative(frozenDist, filename).replaceAll(path.sep, '/'),
            sha256: createHash('sha256')
              .update(await readFile(filename))
              .digest('hex'),
          })
      }
    }
    await collectBuildFiles(frozenDist)
    buildFiles.sort((a, b) => a.path.localeCompare(b.path))
    const revisionOf = (directory) =>
      execFileSync('git', ['rev-parse', 'HEAD'], { cwd: directory, encoding: 'utf8' }).trim()
    const sourceManifest = {
      frontend_revision: revisionOf(frontendDir),
      backend_revision: revisionOf(backendDir),
      frontend_dist_sha256: createHash('sha256').update(JSON.stringify(buildFiles)).digest('hex'),
      openapi_bundle_sha256: createHash('sha256')
        .update(await readFile(path.join(frontendDir, '../api/openapi/openapi-3.0.yaml')))
        .digest('hex'),
      frontend_lock_sha256: createHash('sha256')
        .update(await readFile(path.join(frontendDir, 'pnpm-lock.yaml')))
        .digest('hex'),
      build_files: buildFiles,
    }
    await writeFile(
      path.join(resolvedRunDir, 'source-manifest.json'),
      JSON.stringify(sourceManifest, null, 2),
      'utf8',
    )
    await note(`frontend_build=frozen;sha256=${sourceManifest.frontend_dist_sha256}`)
    await mkdir(path.dirname(binary), { recursive: true })
    await buildBackend(binaryBase, goPath)
    await note('backend_build=passed;output=run/runtime/artifacts/linguaflow')
    const git = sourceManifest.backend_revision
    if (git !== backendRevision)
      throw new Error(`Backend revision mismatch: expected ${backendRevision}, found ${git}`)

    const java = await findJava21()
    await note(`keycloak_java=verified:${java.major}`)
    const keycloak = await startKeycloak(
      runtime,
      cellNames.map((name) => origins[name].publicBase),
      Object.fromEntries(cellNames.map((name) => [name, Object.values(identities[name])])),
      java,
    )
    await note('keycloak=ready;realm_users=isolated_per_cell')

    const oauth = await createB5OAuth({
      callbackBases: cellNames.map((name) => origins[name].publicBase),
      secrets: runtime.secrets,
      identities: cellNames.flatMap((name) => Object.values(identities[name])),
    })
    runtime.servers.push(oauth.server)
    await note('controlled_oauth=ready;subjects=isolated_per_cell')

    const backendRuntime = await import(
      pathToFileURL(path.join(backendDir, 'backend/internal/authacceptance/runtime.mjs')).href
    )

    async function startCell(name) {
      if (!cellNames.includes(name)) throw new Error(`Unknown B5 matrix cell: ${name}`)
      const cellDirectory = path.join(root, safeName(name))
      await mkdir(cellDirectory, { recursive: true })
      const external = name.endsWith('-oidc') ? 'oidc' : 'oauth'
      let postgres
      if (name.startsWith('postgresql')) {
        const owner = await backendRuntime.createRuntime()
        runtime.owners.push(owner)
        const previousBin = process.env.LINGUAFLOW_AUTH_POSTGRES_BIN
        process.env.LINGUAFLOW_AUTH_POSTGRES_BIN = postgresBin
        try {
          postgres = await owner.postgres()
        } finally {
          if (previousBin === undefined) delete process.env.LINGUAFLOW_AUTH_POSTGRES_BIN
          else process.env.LINGUAFLOW_AUTH_POSTGRES_BIN = previousBin
        }
      }

      const crossOrigin = name.startsWith('postgresql')
      const { uiOrigin, publicBase, apiBase } = origins[name]
      const backendPort = ports[name].api
      const adminPassword = secret()
      const jwtSecret = secret()
      const masterKey = randomBytes(32).toString('base64')
      runtime.secrets.push(adminPassword, jwtSecret, masterKey)
      const config = {
        kind: 'server',
        version: 1,
        server: {
          host: '127.0.0.1',
          port: backendPort,
          data_dir: cellDirectory,
          serve_ui: false,
          auto_migrate: true,
          jwt_secret: jwtSecret,
          credentials: { master_key: masterKey },
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
      const configPath = path.join(cellDirectory, 'server.json')
      await writeFile(configPath, JSON.stringify(config), { mode: 0o600 })
      const backendLog = createWriteStream(path.join(cellDirectory, 'backend.log'), {
        flags: 'a',
        mode: 0o600,
      })
      runtime.logs.push({
        name: `${name}-backend.log`,
        stream: backendLog,
        source: path.join(cellDirectory, 'backend.log'),
      })
      const child = spawn(binary, ['serve', '--config', configPath], {
        cwd: cellDirectory,
        env: cleanEnvironment(),
        windowsHide: true,
        stdio: ['ignore', 'pipe', 'pipe'],
      })
      child.stdout.pipe(backendLog)
      child.stderr.pipe(backendLog)
      runtime.children.push(child)
      await ready(`http://127.0.0.1:${backendPort}/health/ready`, child, `${name} backend`)

      const distDir = frozenDist
      const uiServer = await listen(
        createServer((req, res) => {
          if (!crossOrigin && req.url.startsWith('/api/')) {
            void proxy(req, res, backendPort, req.url, '')
            return
          }
          void serveDist(distDir, req, res)
        }),
        ports[name].ui,
      )
      runtime.servers.push(uiServer)
      if (crossOrigin) {
        const gatewayServer = await listen(
          createServer((req, res) => {
            if (req.url.startsWith('/gateway/api/')) {
              void proxy(req, res, backendPort, req.url, '/gateway')
              return
            }
            res.writeHead(404)
            res.end()
          }),
          ports[name].gateway,
        )
        runtime.servers.push(gatewayServer)
      }
      const cell = {
        name,
        deployment: name.startsWith('postgresql') ? 'postgresql' : 'sqlite',
        protocol: external,
        origin: uiOrigin,
        publicBase,
        apiBase,
        adminPassword,
        identities: identities[name],
        keycloak,
        oauth,
        directory: cellDirectory,
        separate_database: true,
        network: crossOrigin
          ? { ui_origin: uiOrigin, api_base: apiBase, gateway: true, same_origin: false }
          : { ui_origin: uiOrigin, api_base: apiBase, same_origin: true },
      }
      await note(`${name}=ready;database=${cell.deployment};subject_prefix=${name}`)
      return cell
    }

    return {
      root,
      runDir: resolvedRunDir,
      backendRevision,
      sourceManifest,
      binary,
      postgresBin,
      secrets: runtime.secrets,
      keycloak,
      oauth,
      identities,
      cellNames,
      startCell,
      async close() {
        if (closed) return
        closed = true
        for (const child of runtime.children.reverse()) {
          if (child.exitCode !== null || !child.pid) continue
          if (process.platform === 'win32')
            await run('taskkill', ['/PID', String(child.pid), '/T', '/F'], {
              stdio: 'ignore',
            }).catch(() => {})
          else child.kill('SIGTERM')
        }
        for (const owner of runtime.owners.reverse()) await owner.close().catch(() => {})
        for (const server of runtime.servers) {
          server.closeAllConnections?.()
          await new Promise((resolve) => server.close(resolve)).catch(() => {})
        }
        for (const item of runtime.logs) {
          const stream = item.stream
          // Child-process pipes may auto-end before close; attach a bounded
          // finish wait and destroy only if the stream never settles.
          if (!stream.destroyed) {
            if (!stream.writableEnded) stream.end()
            if (!stream.writableFinished) {
              const finished = once(stream, 'finish').catch(() => {})
              const timeout = new Promise((resolve) => setTimeout(resolve, 5000))
              await Promise.race([finished, timeout])
              if (!stream.writableFinished) {
                stream.destroy()
                await once(stream, 'close').catch(() => {})
              }
            }
          }
          const source = item.source ?? path.join(root, item.name)
          const target = path.join(resolvedRunDir, 'logs', item.name)
          await mkdir(path.dirname(target), { recursive: true })
          let contents = await readFile(source, 'utf8').catch(() => '')
          for (const value of runtime.secrets)
            if (value) contents = contents.replaceAll(value, '[redacted]')
          await writeFile(target, contents, 'utf8')
        }
        if (!inside(resolvedRunDir, root) || path.basename(root) !== 'runtime')
          throw new Error(`Refusing to remove unmanaged B5 runtime directory: ${root}`)
        try {
          await rm(root, { recursive: true, force: true })
        } catch (error) {
          await note(
            `runtime_cleanup=failed;reason=${String(error?.message ?? error)
              .slice(0, 300)
              .replaceAll('\n', ' ')}`,
          ).catch(() => {})
          throw error
        }
        await note('runtime_cleanup=passed;sanitized_logs=preserved')
      },
    }
  } catch (error) {
    for (const child of runtime.children.reverse()) {
      if (child.exitCode !== null || !child.pid) continue
      if (process.platform === 'win32')
        await run('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' }).catch(
          () => {},
        )
      else child.kill('SIGTERM')
    }
    for (const owner of runtime.owners.reverse()) await owner.close().catch(() => {})
    for (const server of runtime.servers) {
      server.closeAllConnections?.()
      await new Promise((resolve) => server.close(resolve)).catch(() => {})
    }
    throw error
  }
}
