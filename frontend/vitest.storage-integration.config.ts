import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  test: {
    environment: 'node',
    include: ['tests/integration/storage*.test.mjs'],
    fileParallelism: false,
    maxWorkers: 1,
    testTimeout: 60_000,
    hookTimeout: 120_000,
    retry: 0,
    reporters: ['default', 'json'],
    outputFile: { json: 'tests/artifacts/storage-integration/vitest-report.json' },
  },
})
