import assert from 'node:assert/strict'
import { randomBytes } from 'node:crypto'
import { spawn } from 'node:child_process'
import { access, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import path from 'node:path'

const inside = (parent, target) => {
  const relative = path.relative(path.resolve(parent), path.resolve(target))
  return (
    relative !== '' &&
    relative !== '..' &&
    !relative.startsWith(`..${path.sep}`) &&
    !path.isAbsolute(relative)
  )
}

async function command(binary, args, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(binary, args, { cwd, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] })
    let output = ''
    for (const stream of [child.stdout, child.stderr])
      stream.on('data', (chunk) => {
        output += chunk.toString()
      })
    child.once('error', reject)
    child.once('exit', (code) => resolve({ code, output }))
  })
}

async function request(app, endpoint, { method = 'GET', token, body } = {}) {
  const response = await fetch(`${app.apiBase}${endpoint}`, {
    method,
    headers: {
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  const value = await response.json().catch(() => null)
  return { status: response.status, value }
}

/** All material is created inside this run's isolated deployment and deleted after use. */
export async function recoverB5LocalLogin({
  runtime,
  app,
  username = 'admin',
  oldPassword,
  oldSessions = [],
}) {
  const root = path.resolve(runtime.root)
  const directory = path.resolve(app.directory)
  assert.ok(
    path.basename(root) === 'runtime' && path.basename(path.dirname(root)).startsWith('run-'),
  )
  assert.ok(inside(root, directory), 'Recovery target must belong to the new B5 runtime')
  assert.ok(inside(root, runtime.binary), 'Recovery binary must belong to the same isolated run')
  assert.ok(app.separate_database === true, 'Recovery requires a newly isolated database')
  const configPath = path.join(directory, 'server.json')
  await access(configPath)
  const privateDirectory = path.join(directory, `recovery-${randomBytes(5).toString('hex')}`)
  await mkdir(privateDirectory, { recursive: false, mode: 0o700 })
  const password = `B5-Recovered-${randomBytes(24).toString('base64url')}`
  const passwordFile = path.join(privateDirectory, 'new-password')
  const tokenFile = path.join(privateDirectory, 'restore-token')
  const unusedFile = path.join(privateDirectory, 'unused-token')
  const transcript = []
  const cli = async (action, flags, succeeds) => {
    const result = await command(
      runtime.binary,
      ['admin', 'authentication-recovery', action, '--config', configPath, ...flags],
      directory,
    )
    const successful = result.code === 0
    transcript.push({
      command: `linguaflow admin authentication-recovery ${action} --config <isolated-config> ${flags.filter((_, index) => index % 2 === 0).join(' ')} <private-files>`,
      exit_code: result.code,
    })
    if (successful !== succeeds)
      throw new Error(
        `Isolated recovery ${action}: expected ${succeeds ? 'success' : 'rejection'}, exit ${result.code}`,
      )
    if (!succeeds)
      assert.match(result.output, /invalid|used|revoked|expired|changed|无效|已使用|撤销/iu)
  }
  try {
    await writeFile(passwordFile, password, { flag: 'wx', mode: 0o600 })
    await cli('issue', ['--username', username, '--output', tokenFile], true)
    const authorization = (await readFile(tokenFile, 'utf8')).trim()
    assert.match(authorization, /^[\w-]{43}$/)
    await cli(
      'restore-local-login',
      ['--token-file', tokenFile, '--password-file', passwordFile],
      true,
    )
    await cli(
      'restore-local-login',
      ['--token-file', tokenFile, '--password-file', passwordFile],
      false,
    )
    await cli('revoke', ['--token-file', tokenFile], false)

    if (oldPassword) {
      const oldLogin = await request(app, '/auth/login', {
        method: 'POST',
        body: { identifier: username, password: oldPassword },
      })
      assert.equal(oldLogin.status, 401, 'Restored admin accepted the old password')
    }
    const sessionRejections = []
    for (const session of oldSessions) {
      const access = await request(app, '/users/me', { token: session.access_token })
      assert.equal(access.status, 401, 'A target administrator access token survived recovery')
      const refresh = await request(app, '/auth/refresh', {
        method: 'POST',
        body: { refresh_token: session.refresh_token },
      })
      assert.equal(refresh.status, 401, 'A target administrator refresh session survived recovery')
      sessionRejections.push({ access_status: access.status, refresh_status: refresh.status })
    }

    await cli('issue', ['--username', username, '--output', unusedFile], true)
    await cli('revoke', ['--token-file', unusedFile], true)
    await cli(
      'restore-local-login',
      ['--token-file', unusedFile, '--password-file', passwordFile],
      false,
    )

    const discovery = await request(app, '/auth/capabilities')
    assert.equal(discovery.status, 200)
    assert.equal(discovery.value.local_account.can_login, true)
    assert.equal(discovery.value.local_account.can_signup, false)
    assert.equal(discovery.value.local_account.username_login_enabled, true)
    return {
      password,
      evidence: {
        issue_restore: 'passed',
        consumed_authorization_replay: 'rejected',
        consumed_authorization_revoke: 'rejected',
        unused_authorization_revoke: 'passed',
        revoked_authorization_restore: 'rejected',
        old_admin_password: oldPassword ? 'rejected' : 'not_supplied',
        old_sessions: sessionRejections,
        local_login_restored: true,
        local_signup_closed: true,
        transcript,
      },
    }
  } finally {
    assert.ok(inside(directory, privateDirectory) && inside(root, privateDirectory))
    await rm(privateDirectory, { recursive: true, force: true })
  }
}
