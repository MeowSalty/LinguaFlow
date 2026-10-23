import openapiTS, { astToString } from 'openapi-typescript'
import { expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { prepareOpenApiSchema } from '../../scripts/openapi-schema'

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

it('preserves the authentication wire discriminants from the synchronized schema', async () => {
  const bundle = new URL('../../../api/openapi/openapi-3.0.yaml', import.meta.url)
  const schema = prepareOpenApiSchema(readFileSync(bundle, 'utf8'))
  const types = astToString(await openapiTS(schema, { silent: true, defaultNonNullable: false }))
  for (const kind of ['session', 'linked', 'reauthentication_proof', 'test_result']) {
    expect(types).toContain(`kind: "${kind}"`)
  }
  expect(types).toContain('kind: "signup_confirmation" | "link_confirmation"')
  expect(types).not.toMatch(/kind: "External\w+Response"/)
})
