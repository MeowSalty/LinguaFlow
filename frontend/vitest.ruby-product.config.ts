import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'node',
    include: ['tests/integration/ruby-product.test.mjs'],
    fileParallelism: false,
    maxWorkers: 1,
    testTimeout: 180_000,
    hookTimeout: 120_000,
    retry: 0,
    reporters: ['default', 'json'],
    outputFile: { json: 'tests/artifacts/ruby-product/vitest-report.json' },
  },
})
