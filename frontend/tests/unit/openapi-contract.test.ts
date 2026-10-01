import openapiTS, { astToString } from 'openapi-typescript'
import { expect, it } from 'vitest'

it('generates the same types from the OpenAPI source and synchronized bundle', async () => {
  const source = new URL('../../../api/openapi/base.yaml', import.meta.url)
  const bundle = new URL('../../../api/openapi/openapi-3.0.yaml', import.meta.url)
  const options = { alphabetize: true, silent: true, defaultNonNullable: false }
  const [sourceTypes, bundledTypes] = await Promise.all([
    openapiTS(source, options).then(astToString),
    openapiTS(bundle, options).then(astToString),
  ])
  expect(sourceTypes).toBe(bundledTypes)
})
