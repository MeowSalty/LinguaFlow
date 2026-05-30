/**
 * 使用 openapi-typescript 程序化 API 生成前端 TypeScript 类型定义。
 *
 * 通过 transform 选项将 `type: string, format: binary`
 * 映射为 TypeScript 的 `File` 类型，而非默认的 `string`。
 *
 * 用法：在 frontend/ 目录下执行
 *   node --import jiti/register scripts/generate-openapi-types.ts
 */
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import fs from 'node:fs'
import openapiTS, { astToString, COMMENT_HEADER } from 'openapi-typescript'
import ts from 'typescript'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const INPUT = new URL(`file://${path.resolve(__dirname, '../../api/openapi/openapi-3.0.yaml')}`)
const OUTPUT = path.resolve(__dirname, '../src/api/types.d.ts')

const FILE = ts.factory.createIdentifier('File')
const NULL = ts.factory.createLiteralTypeNode(ts.factory.createNull())

const ast = await openapiTS(INPUT, {
  transform(schemaObject) {
    if (schemaObject.format === 'binary') {
      return schemaObject.nullable
        ? ts.factory.createUnionTypeNode([FILE, NULL])
        : FILE
    }
  },
})

fs.mkdirSync(path.dirname(OUTPUT), { recursive: true })
fs.writeFileSync(OUTPUT, `${COMMENT_HEADER}${astToString(ast)}`, 'utf8')
console.log(`✅ Generated ${path.relative(process.cwd(), OUTPUT)}`)
