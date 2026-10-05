import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    include: ['tests/integration/configuration.test.mjs'],
    fileParallelism: false,
    maxWorkers: 1,
    testTimeout: 60_000,
    hookTimeout: 120_000,
    retry: 0,
    reporters: ['default', 'json'],
    outputFile: { json: 'tests/artifacts/configuration-integration/vitest-report.json' },
  },
})
