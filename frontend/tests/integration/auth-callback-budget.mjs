import assert from 'node:assert/strict'
import { readFile, mkdir, writeFile, stat } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

const frontend = new URL('../../', import.meta.url)
const manifest = JSON.parse(await readFile(new URL('dist/.vite/manifest.json', frontend), 'utf8'))
const collect = (roots) => {
  const keys = new Set()
  const visit = (key) => {
    if (keys.has(key)) return
    assert.ok(manifest[key], `Missing build entry: ${key}`)
    keys.add(key)
    for (const child of manifest[key].imports ?? []) visit(child)
  }
  for (const root of roots) visit(root)
  return keys
}
const measure = async (roots) => {
  const keys = collect(roots)
  const files = [...new Set([...keys].map((key) => manifest[key].file))]
  const bytes = await Promise.all(
    files.map(async (file) => (await stat(new URL(`dist/${file}`, frontend))).size),
  )
  return {
    modules: [...keys],
    files,
    requests: files.length,
    bytes: bytes.reduce((sum, size) => sum + size, 0),
  }
}
const html = await readFile(new URL('dist/index.html', frontend), 'utf8')
assert.ok(
  html.includes('正在确认认证结果…'),
  'HTML must display status without downloading modules',
)
assert.ok(!html.includes('%AUTH_CALLBACK_'), 'Callback messages must be compiled into HTML')
assert.equal(
  (html.match(/rel="stylesheet"/g) ?? []).length,
  0,
  'Business CSS must not block the callback',
)

const relay = await measure(['index.html', 'src/auth/callback-entry.ts'])
const exchange = await measure([
  'index.html',
  'src/auth/callback-entry.ts',
  'src/auth/callback-exchange.ts',
])
for (const graph of [relay, exchange]) {
  for (const key of graph.modules) {
    assert.ok(
      !['src/main.ts', 'src/i18n.ts', 'src/App.vue', 'src/api/session-context.ts'].includes(key),
      `Heavy startup dependency: ${key}`,
    )
    assert.doesNotMatch(
      key,
      /(?:runtime-core|vue-i18n|AppLayout|naive-ui|pinia)/,
      `Heavy startup dependency: ${key}`,
    )
  }
}
assert.ok(relay.bytes <= 20_000, `Relay budget exceeded: ${relay.bytes} bytes`)
assert.ok(exchange.bytes <= 60_000, `Exchange budget exceeded: ${exchange.bytes} bytes`)
const output = new URL('test-results/auth-complete-improvement/bundle-budget.json', frontend)
await mkdir(new URL('.', output), { recursive: true })
await writeFile(
  output,
  `${JSON.stringify({ htmlBytes: Buffer.byteLength(html), relay, exchange }, null, 2)}\n`,
)
console.log(
  `callback HTML: ${Buffer.byteLength(html)} bytes; relay: ${relay.requests} JS / ${relay.bytes} bytes; exchange: ${exchange.requests} JS / ${exchange.bytes} bytes`,
)
console.log(`Evidence: ${fileURLToPath(output)}`)
