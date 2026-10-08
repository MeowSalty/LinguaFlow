import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    include: ['tests/integration/task-history.test.mjs'],
    fileParallelism: false,
    maxWorkers: 1,
    testTimeout: 120_000,
    hookTimeout: 180_000,
    retry: 0,
    reporters: ['default', 'json'],
    outputFile: { json: 'tests/artifacts/task-history-integration/vitest-report.json' },
  },
})
