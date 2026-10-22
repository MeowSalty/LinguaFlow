/**
 * 使用 openapi-typescript 程序化 API 生成前端 TypeScript 类型定义。
 *
 * 在内存中补齐联合响应的 discriminator 映射，保留合同原有的 kind 枚举。
 * 不改写 api/ 下的源文件或 bundle。
 *
 * 用法：在 frontend/ 目录下执行
 *   node --import jiti/register scripts/generate-openapi-types.ts
 */
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import fs from 'node:fs'
import openapiTS, { astToString, COMMENT_HEADER } from 'openapi-typescript'
import { prepareOpenApiSchema } from './openapi-schema'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const INPUT = path.resolve(__dirname, '../../api/openapi/openapi-3.0.yaml')
const OUTPUT = path.resolve(__dirname, '../src/api/types.d.ts')

const ast = await openapiTS(prepareOpenApiSchema(fs.readFileSync(INPUT, 'utf8')), {
  defaultNonNullable: false,
})

fs.mkdirSync(path.dirname(OUTPUT), { recursive: true })
fs.writeFileSync(OUTPUT, `${COMMENT_HEADER}${astToString(ast)}`, 'utf8')
console.log(`✅ Generated ${path.relative(process.cwd(), OUTPUT)}`)
