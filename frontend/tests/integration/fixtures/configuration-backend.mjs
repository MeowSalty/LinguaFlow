import assert from 'node:assert/strict'
import { spawn, execFileSync } from 'node:child_process'
import { createHash, randomBytes, randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { once } from 'node:events'
import { chmod, mkdir, mkdtemp, writeFile } from 'node:fs/promises'
import { createServer } from 'node:http'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'

const frontend = fileURLToPath(new URL('../../../', import.meta.url))
const backend = path.resolve(
  process.env.LINGUAFLOW_TEST_BACKEND ?? path.join(frontend, '../../LinguaFlow-backend'),
)

// Build once per suite; every test owns a separate server, database and upstream.
export function buildConfigurationBackend() {
  const backendCommit = execFileSync('git', ['rev-parse', 'HEAD'], {
    cwd: backend,
    encoding: 'utf8',
    windowsHide: true,
    timeout: 10_000,
  }).trim()
  if (process.platform === 'win32') {
    execFileSync(
      'powershell.exe',
      ['-NoProfile', '-Command', 'task backend:build; exit $LASTEXITCODE'],
      { cwd: backend, stdio: 'inherit', windowsHide: true, timeout: 120_000 },
    )
  } else {
    execFileSync('task', ['backend:build'], {
      cwd: backend,
      stdio: 'inherit',
      windowsHide: true,
      timeout: 120_000,
    })
  }
  const digest = (file) => createHash('sha256').update(readFileSync(file)).digest('hex')
  return {
    backendCommit,
    backend,
    backendBinarySha256: digest(
      path.join(
        backend,
        'backend/bin',
        process.platform === 'win32' ? 'linguaflow.exe' : 'linguaflow',
      ),
    ),
    backendContractSha256: digest(path.join(backend, 'api/openapi/openapi-3.0.yaml')),
    frontendContractSha256: digest(path.join(frontend, '../api/openapi/openapi-3.0.yaml')),
  }
}

async function restrictKeyring(file) {
  if (process.platform !== 'win32') return chmod(file, 0o600)
  execFileSync(
    'powershell.exe',
    [
      '-NoProfile',
      '-Command',
      [
        '$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User',
        '$acl = New-Object System.Security.AccessControl.FileSecurity',
        '$acl.SetOwner($sid)',
        '$acl.SetAccessRuleProtection($true, $false)',
        '$acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new($sid, "FullControl", "Allow"))',
        '$acl.AddAccessRule([System.Security.AccessControl.FileSystemAccessRule]::new([System.Security.Principal.SecurityIdentifier]::new("S-1-5-18"), "FullControl", "Allow"))',
        'Set-Acl -LiteralPath $env:LF_CONFIGURATION_TEST_KEYRING -AclObject $acl',
      ].join('; '),
    ],
    {
      env: { ...process.env, LF_CONFIGURATION_TEST_KEYRING: file },
      windowsHide: true,
      timeout: 10_000,
    },
  )
}

export async function createConfigurationBackend(metadata, signal, options = {}) {
  const category = options.storage ? 'storage-integration' : 'configuration-integration'
  const resultsRoot = path.join(frontend, 'tests/artifacts', category)
  await mkdir(resultsRoot, { recursive: true })
  const runDir = await mkdtemp(path.join(resultsRoot, `${category}-`))
  const report = {
    ...metadata,
    mode: 'serve',
    database: 'isolated SQLite',
    upstream: 'local OpenAI-compatible HTTP fixture',
    limitations: [],
    externalProvider: 'not tested',
  }
  const tokenSecrets = [
    randomBytes(24).toString('hex'),
    randomBytes(24).toString('hex'),
    randomBytes(24).toString('hex'),
  ]
  const calls = []
  const holds = new Set()
  const releases = new Map()
  const upstreamErrors = []
  let serverProcess
  let serverOutput = ''
  let apiBase
  let adminToken
  let authSession
  let endpoint
  let config
  let env
  let serverError

  async function until(read, predicate, label, timeout = 20000) {
    const deadline = Date.now() + timeout
    let value
    while (Date.now() < deadline) {
      signal?.throwIfAborted()
      value = await read()
      if (predicate(value)) return value
      await delay(60)
    }
    throw new Error(`Timed out: ${label}; last=${JSON.stringify(value)}`)
  }
  async function listen(server) {
    server.listen(0, '127.0.0.1')
    await once(server, 'listening')
    return server.address().port
  }
  function launchBackend(config, env) {
    serverProcess = spawn(
      path.join(backend, `backend/bin/linguaflow${process.platform === 'win32' ? '.exe' : ''}`),
      ['serve', '--config', config],
      { cwd: runDir, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] },
    )
    serverError = undefined
    serverProcess.on('error', (error) => {
      serverError = error
    })
    serverProcess.stdout.on('data', (chunk) => {
      serverOutput += chunk.toString()
    })
    serverProcess.stderr.on('data', (chunk) => {
      serverOutput += chunk.toString()
    })
    return until(
      async () => {
        if (serverError) throw serverError
        if (serverProcess.exitCode != null) throw new Error(`Backend exited: ${serverOutput}`)
        try {
          return await request('GET', '/ping')
        } catch {
          return null
        }
      },
      Boolean,
      'backend ready',
      30000,
    )
  }
  async function stopBackend() {
    if (!serverProcess?.pid || serverProcess.exitCode != null || serverProcess.signalCode != null)
      return
    const child = serverProcess
    const stopped = once(child, 'exit', { signal: AbortSignal.timeout(5000) })
    child.kill()
    try {
      await stopped
    } catch {
      if (child.exitCode != null || child.signalCode != null) return
      const forced = once(child, 'exit', { signal: AbortSignal.timeout(5000) })
      child.kill('SIGKILL')
      await forced
    }
  }
  function release(model) {
    holds.delete(model)
    for (const done of releases.get(model) ?? []) done()
    releases.delete(model)
  }
  const upstream = createServer(async (req, res) => {
    try {
      if (req.url?.endsWith('/models')) {
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({ data: [{ id: 'fixture-model', object: 'model' }] }))
        return
      }
      const chunks = []
      for await (const chunk of req) chunks.push(chunk)
      const body = JSON.parse(Buffer.concat(chunks).toString())
      const user = body.messages.findLast((message) => message.role === 'user')
      const envelope = JSON.parse(user.content)
      calls.push({
        model: body.model,
        secretVersion:
          tokenSecrets.indexOf(String(req.headers.authorization).replace(/^Bearer /, '')) + 1,
        at: Date.now(),
      })
      if (holds.has(body.model))
        await new Promise((resolve) =>
          releases.set(body.model, [...(releases.get(body.model) ?? []), resolve]),
        )
      const translations = Object.fromEntries(
        Object.entries(envelope.segments)
          .filter(([, segment]) => segment.translate)
          .map(([id]) => [id, '测试译文']),
      )
      res.writeHead(200, { 'Content-Type': 'application/json' })
      res.end(
        JSON.stringify({
          id: 'integration',
          object: 'chat.completion',
          choices: [
            {
              index: 0,
              message: { role: 'assistant', content: JSON.stringify({ translations }) },
              finish_reason: 'stop',
            },
          ],
          usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
        }),
      )
    } catch (error) {
      upstreamErrors.push(error.message)
      res.writeHead(500)
      res.end('Fixture protocol error')
    }
  })
  async function request(method, route, body, expected = 200, token = adminToken) {
    const headers = token ? { Authorization: `Bearer ${token}` } : {}
    if (method === 'POST' && /^\/projects\/\d+\/resources$/.test(route))
      headers['Idempotency-Key'] = randomUUID()
    if (body !== undefined && !(body instanceof FormData))
      headers['Content-Type'] = 'application/json'
    const init = {
      method,
      headers,
      signal: AbortSignal.any([AbortSignal.timeout(15000), ...(signal ? [signal] : [])]),
    }
    if (body !== undefined) {
      assert.ok(method !== 'GET' && method !== 'HEAD', 'Read requests cannot carry a body')
      init.body = body instanceof FormData ? body : JSON.stringify(body)
    }
    const response = await fetch(`${apiBase}${route}`, init)
    const raw = await response.text()
    const data = raw ? JSON.parse(raw) : undefined
    if (expected != null) assert.equal(response.status, expected, `${method} ${route}: ${raw}`)
    return expected == null ? { status: response.status, data } : data
  }
  const getJob = (id) => request('GET', `/jobs/${id}`)
  const waitJob = (id, status) =>
    until(
      () => getJob(id),
      (job) => job.status === status,
      `job ${id} ${status}`,
    )
  const terminalJob = (id) =>
    until(
      () => getJob(id),
      (job) => ['completed', 'failed', 'cancelled'].includes(job.status),
      `job ${id} terminal`,
    )
  const backendBody = (name, model, endpoint, credentialId, rpm = 0) => ({
    name,
    type: 'openai',
    options: {
      type: 'openai',
      model,
      base_url: endpoint,
      response_format: 'json_schema',
      timeout: 10,
    },
    rate_limit_per_minute: rpm,
    ...(credentialId ? { credential_id: credentialId } : {}),
  })
  async function makeProject(name) {
    const project = await request(
      'POST',
      '/projects',
      { name, source_lang: 'en', target_lang: 'zh', glossary_enabled: false },
      201,
    )
    const form = new FormData()
    form.append(
      'files',
      new Blob(
        [
          JSON.stringify({
            first: `${name} first line`,
            second: `${name} second line`,
            third: `${name} third line`,
          }),
        ],
        { type: 'application/json' },
      ),
      `${name}.json`,
    )
    const uploaded = await request('POST', `/projects/${project.id}/resources`, form)
    assert.equal(uploaded.items[0].action, 'created')
    assert.equal(uploaded.items[0].resource.total_segments, 3)
    return project
  }
  async function makePlan(name, backendId, profileId) {
    return request(
      'POST',
      '/execution-plan-templates',
      {
        name,
        profile_id: profileId,
        rounds: [
          {
            mode: 'translate',
            backend_id: backendId,
            concurrency: 1,
            translate: {
              prompt_template_id: -1,
              batch_size: 1,
              max_words_per_batch: 0,
              fallback_shrink: 1,
              retry: { max_attempts: 1, backoff_ms: 0, jitter: false },
            },
          },
        ],
      },
      201,
    )
  }
  const startJob = (project, plan) =>
    request(
      'POST',
      `/projects/${project.id}/jobs`,
      { execution_plan_id: plan.id, segment_filter: 'all' },
      202,
    )
  async function pausedJob(project, plan, model) {
    holds.add(model)
    const before = calls.length
    const job = await startJob(project, plan)
    await until(
      () => calls.length,
      (count) => count > before,
      `${model} first upstream request`,
    )
    await request('POST', `/jobs/${job.id}/pause`)
    release(model)
    await waitJob(job.id, 'paused')
    return job
  }
  async function expectRecoveryBlocked(job, reason) {
    const before = calls.length
    const logOffset = serverOutput.length
    const result = await request('POST', `/jobs/${job.id}/resume`, undefined, null)
    if (result.status === 200) assert.equal((await terminalJob(job.id)).status, 'failed')
    else if (result.status === 500) {
      await until(
        () => serverOutput.slice(logOffset),
        (log) => log.includes(reason),
        'server rejection reason',
      )
      report.limitations.push(
        `Resume after ${reason} returns generic HTTP 500; rejection confirmed by server log and no upstream requests.`,
      )
      assert.equal((await getJob(job.id)).status, 'paused')
    } else
      assert.ok(
        [400, 403, 409, 422].includes(result.status),
        `Unexpected resume status ${result.status}`,
      )
    assert.equal(calls.length, before, 'blocked recovery reached upstream')
  }

  async function start() {
    const upstreamPort = await listen(upstream)
    endpoint = `http://127.0.0.1:${upstreamPort}/v1`
    const reservation = createServer()
    const port = await listen(reservation)
    await new Promise((resolve) => reservation.close(resolve))
    apiBase = `http://127.0.0.1:${port}/api/v1`
    const password = `Test-${randomBytes(16).toString('hex')}!`
    const keyring = path.join(runDir, 'keyring.json')
    await writeFile(
      keyring,
      JSON.stringify({
        version: 1,
        active_key_id: 'integration',
        keys: { integration: randomBytes(32).toString('base64') },
      }),
    )
    await restrictKeyring(keyring)
    config = path.join(runDir, 'server.yaml')
    await writeFile(
      config,
      JSON.stringify(
        {
          kind: 'server',
          version: 1,
          server: {
            host: '127.0.0.1',
            port,
            data_dir: path.join(runDir, 'data'),
            serve_ui: false,
            jwt_secret: randomBytes(32).toString('hex'),
            credentials: { keyring_file: keyring },
            workers: { translation: { count: 2 }, sync: { count: 1 } },
            ...(options.storage
              ? {
                  storage: {
                    enabled: true,
                    default_site_space: 'local',
                    backends: [
                      { id: 'local', driver: 'local', root: path.join(runDir, 'objects') },
                    ],
                    work_dir: path.join(runDir, 'storage-work'),
                    cache_dir: path.join(runDir, 'storage-cache'),
                  },
                }
              : {}),
          },
          log: { level: 'warn' },
          bootstrap: {
            registration_enabled: false,
            admin: { username: 'integration-admin', email: 'integration@example.test', password },
          },
        },
        null,
        2,
      ),
    )
    env = Object.fromEntries(
      Object.entries(process.env).filter(([key]) => !key.startsWith('LINGUAFLOW_')),
    )
    await launchBackend(config, env)
    authSession = await request(
      'POST',
      '/auth/login',
      { username: 'integration-admin', password },
      200,
      null,
    )
    adminToken = authSession.access_token
  }
  async function restart() {
    await stopBackend()
    await launchBackend(config, env)
  }
  async function close() {
    for (const model of holds) release(model)
    try {
      await stopBackend()
    } finally {
      upstream.closeAllConnections()
      if (upstream.listening) await new Promise((resolve) => upstream.close(resolve))
      await writeFile(path.join(runDir, 'backend.log'), serverOutput)
    }
  }
  async function writeReport(task) {
    report.scenarios = [
      { name: task.name, status: task.result?.state === 'pass' ? 'passed' : 'failed' },
    ]
    await writeFile(path.join(runDir, 'report.json'), JSON.stringify(report, null, 2))
    console.log('Integration artifacts:', runDir)
  }
  async function makeProfile(name = 'Integration profile') {
    return request(
      'POST',
      '/execution-profiles',
      {
        name,
        config: {
          schema_version: 1,
          ruby: { enabled: false, preserve_kinds: [] },
          context: { enabled: false, before: 0, after: 0 },
          qa: { enabled: false, checks: [] },
        },
      },
      201,
    )
  }
  async function makeWorkflow(model = 'fixture-model', rpm = 0) {
    const profile = await makeProfile()
    const credential = await request(
      'POST',
      '/credentials',
      { provider: 'openai', endpoint, secret: tokenSecrets[0] },
      201,
    )
    const asset = await request(
      'POST',
      '/backends',
      backendBody('Test backend', model, endpoint, credential.id, rpm),
      201,
    )
    const plan = await makePlan('Test plan', asset.id, profile.id)
    const project = await makeProject('test-project')
    return { profile, credential, backend: asset, plan, project }
  }
  return {
    start,
    runDir,
    restart,
    close,
    writeReport,
    request,
    until,
    getJob,
    terminalJob,
    backendBody,
    makeProject,
    makePlan,
    makeProfile,
    makeWorkflow,
    startJob,
    pausedJob,
    expectRecoveryBlocked,
    tokenSecrets,
    calls,
    holds,
    release,
    upstreamErrors,
    get endpoint() {
      return endpoint
    },
    get apiBase() {
      return apiBase
    },
    get authSession() {
      return authSession
    },
    recordObservation(name, facts) {
      report.observations ??= []
      report.observations.push({ name, facts })
    },
  }
}
