import { parse } from 'yaml'
import type { OpenAPI3 } from 'openapi-typescript'

/** Preserve explicit wire enums when a union omits its discriminator mapping. */
export function prepareOpenApiSchema(source: string): OpenAPI3 {
  const document = parse(source) as OpenAPI3
  const resolve = (ref: string): unknown => {
    if (!ref.startsWith('#/')) return undefined
    let value: unknown = document
    for (const part of ref.slice(2).split('/')) {
      if (!value || typeof value !== 'object') return undefined
      value = (value as Record<string, unknown>)[part.replace(/~1/g, '/').replace(/~0/g, '~')]
    }
    return value
  }
  function visit(value: unknown) {
    if (!value || typeof value !== 'object') return
    const node = value as Record<string, unknown>
    const discriminator = node.discriminator as
      | { propertyName?: string; mapping?: Record<string, string> }
      | undefined
    if (discriminator?.propertyName && !discriminator.mapping && Array.isArray(node.oneOf)) {
      const mapping: Record<string, string> = {}
      let complete = true
      for (const variant of node.oneOf) {
        const ref = (variant as { $ref?: string }).$ref
        const schema = ref
          ? (resolve(ref) as { properties?: Record<string, { enum?: unknown[] }> } | undefined)
          : undefined
        const values = schema?.properties?.[discriminator.propertyName]?.enum
        if (!ref || !values?.length || values.some((item) => typeof item !== 'string')) {
          complete = false
          break
        }
        for (const item of values as string[]) {
          if (mapping[item] && mapping[item] !== ref)
            throw new Error(`Ambiguous discriminator value: ${item}`)
          mapping[item] = ref
        }
      }
      if (complete) discriminator.mapping = mapping
    }
    for (const child of Object.values(node)) visit(child)
  }
  visit(document)
  return document
}
