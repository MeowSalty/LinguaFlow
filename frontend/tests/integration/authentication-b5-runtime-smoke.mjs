import assert from 'node:assert/strict'
import { writeFile } from 'node:fs/promises'
import path from 'node:path'
import { createB5RunDirectory, createB5Runtime } from './authentication-b5-runtime.mjs'

// Read-only dependency smoke: no product UI acceptance or deployment recovery claims.
const runDir = await createB5RunDirectory()
const report = { kind: 'runtime-smoke', product_acceptance: false, cells: {}, status: 'running' }
console.log(`B5 runtime smoke: ${runDir}`)
let runtime
try {
  runtime = await createB5Runtime({ runDir })
  report.backend_revision = runtime.backendRevision
  for (const name of runtime.cellNames) {
    const app = await runtime.startCell(name)
    const response = await fetch(`${app.apiBase}/auth/capabilities`)
    assert.equal(response.status, 200, `${name}: capability discovery failed`)
    const capabilities = await response.json()
    assert.equal(capabilities.mode, 'server')
    assert.equal(typeof capabilities.local_account?.can_login, 'boolean')
    assert.equal(capabilities.local_account.signup.password_requirement, 'required')
    assert.deepEqual(capabilities.connections, [])
    report.cells[name] = {
      status: 'passed',
      deployment: app.deployment,
      protocol: app.protocol,
      same_origin: app.network.same_origin,
      gateway: app.network.gateway ?? false,
      separate_database: app.separate_database,
    }
    console.log(`${name}: database, gateway and capability discovery passed`)
  }
  report.status = 'passed'
} catch (error) {
  report.status = 'failed'
  report.reason = error instanceof Error ? error.message : 'Runtime failed'
  process.exitCode = 1
  console.error(report.reason)
} finally {
  if (runtime) await runtime.close()
  await writeFile(path.join(runDir, 'runtime-smoke-report.json'), JSON.stringify(report, null, 2))
}
