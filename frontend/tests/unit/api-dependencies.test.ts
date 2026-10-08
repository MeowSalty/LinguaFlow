import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'

const frontendRoot = fileURLToPath(new URL('../../', import.meta.url))
const apiRoot = join(frontendRoot, 'src/api')
const publicEntry = join(apiRoot, 'client.ts')
const configPath = join(frontendRoot, 'tsconfig.app.json')
const config = ts.readConfigFile(configPath, ts.sys.readFile)
const { options } = ts.parseJsonConfigFileContent(config.config, ts.sys, frontendRoot)
const displayPath = (path: string) => relative(frontendRoot, path).replaceAll('\\', '/')

interface Dependency {
  specifier: string
  typeOnly: boolean
}

function dependencies(source: string, filename: string): Dependency[] {
  const file = ts.createSourceFile(filename, source, ts.ScriptTarget.Latest, true)
  const result: Dependency[] = []
  const add = (specifier: ts.Node | undefined, typeOnly: boolean) => {
    if (specifier && ts.isStringLiteralLike(specifier)) {
      result.push({ specifier: specifier.text, typeOnly })
    }
  }
  const visit = (node: ts.Node) => {
    if (ts.isImportDeclaration(node)) {
      const clause = node.importClause
      const bindings = clause?.namedBindings
      const onlyNamedTypes =
        !clause?.name &&
        bindings &&
        ts.isNamedImports(bindings) &&
        bindings.elements.length > 0 &&
        bindings.elements.every((element) => element.isTypeOnly)
      add(node.moduleSpecifier, Boolean(clause?.isTypeOnly || onlyNamedTypes))
    } else if (ts.isExportDeclaration(node)) {
      const clause = node.exportClause
      const onlyNamedTypes =
        clause &&
        ts.isNamedExports(clause) &&
        clause.elements.length > 0 &&
        clause.elements.every((element) => element.isTypeOnly)
      add(node.moduleSpecifier, Boolean(node.isTypeOnly || onlyNamedTypes))
    } else if (ts.isImportTypeNode(node) && ts.isLiteralTypeNode(node.argument)) {
      add(node.argument.literal, true)
    } else if (
      ts.isImportEqualsDeclaration(node) &&
      ts.isExternalModuleReference(node.moduleReference)
    ) {
      add(node.moduleReference.expression, node.isTypeOnly)
    } else if (ts.isCallExpression(node) && node.expression.kind === ts.SyntaxKind.ImportKeyword) {
      add(node.arguments[0], false)
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  return result
}

function isSourceModule(path: string): boolean {
  return /\.[cm]?[jt]sx?$/.test(path) && !/\.d\.[cm]?ts$/.test(path)
}

function apiModules(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    if (entry.name === '__tests__' || /\.(test|spec)\./.test(entry.name)) return []
    const path = join(directory, entry.name)
    if (entry.isDirectory()) return apiModules(path)
    return isSourceModule(path) ? [path] : []
  })
}

function resolveLocal(specifier: string, importer: string): string | undefined {
  if (!specifier.startsWith('.') && !specifier.startsWith('@/')) return
  const resolved = ts.resolveModuleName(specifier, importer, options, ts.sys).resolvedModule
  if (!resolved) {
    throw new Error(`Cannot resolve ${specifier} imported by ${displayPath(importer)}`)
  }
  return resolve(resolved.resolvedFileName)
}

function dependencyGraph(roots: string[]) {
  const graph = new Map<string, string[]>()
  const entryImports = new Set<string>()
  const visit = (file: string) => {
    if (graph.has(file)) return
    const edges: string[] = []
    graph.set(file, edges)
    for (const dependency of dependencies(readFileSync(file, 'utf8'), file)) {
      const target = resolveLocal(dependency.specifier, file)
      if (!target) continue
      // Even type-only references to the barrel violate the internal boundary.
      if (file !== publicEntry && target === publicEntry) {
        entryImports.add(`${displayPath(file)} -> ${displayPath(target)}`)
      }
      if (dependency.typeOnly || !isSourceModule(target)) continue
      edges.push(target)
      visit(target)
    }
  }
  roots.forEach(visit)
  return { graph, entryImports: [...entryImports].sort() }
}

function cyclePaths(graph: Map<string, string[]>): string[] {
  const visited = new Set<string>()
  const active: string[] = []
  const cycles: string[] = []
  const visit = (file: string) => {
    const cycleStart = active.indexOf(file)
    if (cycleStart !== -1) {
      cycles.push([...active.slice(cycleStart), file].map(displayPath).join(' -> '))
      return
    }
    if (visited.has(file)) return
    visited.add(file)
    active.push(file)
    for (const target of graph.get(file) ?? []) visit(target)
    active.pop()
  }
  for (const file of graph.keys()) visit(file)
  return cycles.sort()
}

describe('API dependency boundaries', () => {
  const modules = apiModules(apiRoot)
  const { graph, entryImports } = dependencyGraph(modules)

  it('keeps every API module and its reachable local dependencies free of runtime cycles', () => {
    const cycles = cyclePaths(graph)
    expect(cycles, `Runtime dependency cycles:\n${cycles.join('\n')}`).toEqual([])
  })

  it('prevents internal modules from importing the public client barrel', () => {
    expect(entryImports, `Import client-core directly:\n${entryImports.join('\n')}`).toEqual([])
  })

  it('distinguishes erased type references from runtime imports and re-exports', () => {
    const refs = dependencies(
      `import type { A } from './types-a'
       import { type B } from './types-b'
       export type * from './types-c'
       export { type D } from './types-d'
       type E = import('./types-e').E
       import './side-effect'
       import value, { type F } from './mixed-default'
       import { value as renamed, type G } from './mixed-named'
       export * from './re-export'
       export { value, type H } from './mixed-export'
       const lazy = () => import('./lazy')`,
      join(dirname(publicEntry), 'example.ts'),
    )
    expect(
      refs.filter((dependency) => dependency.typeOnly).map(({ specifier }) => specifier),
    ).toEqual(['./types-a', './types-b', './types-c', './types-d', './types-e'])
    expect(
      refs.filter((dependency) => !dependency.typeOnly).map(({ specifier }) => specifier),
    ).toEqual([
      './side-effect',
      './mixed-default',
      './mixed-named',
      './re-export',
      './mixed-export',
      './lazy',
    ])
  })
})
