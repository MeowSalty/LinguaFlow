# 翻译配置 · 参考

本页为 **检索型参考**：AI 后端选项、提示词变量、执行配置字段、执行计划轮次字段与默认值。

::: tip 使用说明
界面步骤与场景见 [翻译配置 · 使用](/zh/guide/translation-config)。行为原理见 [流水线与原理](/zh/guide/pipeline)。CLI 的 `linguaflow.yaml` 结构见 [配置文件与环境变量](/zh/guide/configuration)。
:::

## AI 后端

### 类型与默认模型

| 后端 | 类型标识 | 默认模型 | 说明 |
| --- | --- | --- | --- |
| OpenAI | `openai` | `gpt-4o-mini` | 兼容 Azure / Ollama / LM Studio 等 OpenAI API |
| Anthropic | `anthropic` | `claude-sonnet-4-5` | Tool Use 结构化输出；可选提示缓存 |
| Google Gemini | `google` | `gemini-2.5-flash` | ResponseMIMEType 结构化输出 |

### 通用 options

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `api_key` | string | **必填** | API 密钥，支持 `${ENV_VAR}` |
| `base_url` | string | SDK 默认 | 自定义端点 |
| `model` | string | 见上表 | 模型名 |
| `max_tokens` | int | OpenAI: `0`；Anthropic/Gemini: `8192` | 最大生成 token；`0` 常表示不额外限制 |
| `timeout` | int/string | `60`（秒） | 秒数或 Go duration |
| `response_format` | string | `json_schema` | `json_schema` \| `json_object` \| `text` \| `none` |
| `temperature` | float | API 默认 | 采样温度 |
| `top_p` | float | API 默认 | 核采样 |
| `stream` | bool | `false` | 上游流式请求，内部累积为完整响应 |
| `rate_limit_per_minute` | int | `0` | 每分钟请求上限；`0` 不限 |

### Anthropic 专有

| 选项 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enable_prompt_cache` | bool | `true` | system prompt 缓存断点，降低 token 消耗 |

### Base URL 示例

| 服务 | 示例 |
| --- | --- |
| Azure OpenAI | `https://<resource>.openai.azure.com/openai/deployments/<model>` |
| Ollama | `http://localhost:11434/v1` |
| LM Studio | `http://localhost:1234/v1` |

---

## 提示词模板

### 类型

| 类型 | 用途 |
| --- | --- |
| 翻译提示词 | 翻译阶段 system prompt |
| 术语抽取（Bootstrap） | 提取术语阶段 |
| 术语精简（Prune） | 术语表清理阶段 |

Web 中在对应资源页管理；内置模板 scope 为 `system`，不可改删。

### 翻译提示词变量

::: v-pre

| 变量 | 类型 | 说明 |
| --- | --- | --- |
| `{{.SourceLang}}` | string | 源语言，`auto` 为自动检测 |
| `{{.TargetLang}}` | string | 目标语言（BCP 47） |
| `{{.Source}}` | string | 单段模式源文本 |
| `{{.Segments}}` | []SegmentInput | 批量段落：`ID` / `Source` / `Translate` |
| `{{.Glossary}}` | []GlossaryEntry | 术语：`Source` / `Target` / `Notes` |
| `{{.TMHints}}` | []TMHint | 翻译记忆：`Source` / `Target` / `Score` |
| `{{.TextMode}}` | bool | `true` 纯文本编号；`false` JSON envelope |
| `{{.StrictSchema}}` | bool | 后端 json_schema 强制时精简协议描述 |
| `{{.InlineBootstrap}}` | bool | 是否内联抽术语 |
| `{{.MaxBootstrapTerms}}` | int | 内联术语上限 |
| `{{.HasRuby}}` | bool | 是否有 Ruby 注音 |
| `{{.RubyMode}}` | string | `json` \| `section` \| `inline` |
| `{{.RubyAnnotations}}` | map | 段落 ID → 注音列表 |

:::

### 术语抽取变量

::: v-pre

| 变量 | 类型 | 说明 |
| --- | --- | --- |
| `{{.SourceLang}}` | string | 源语言 |
| `{{.TargetLang}}` | string | 目标语言 |
| `{{.MaxTerms}}` | int | 最多抽取条数 |
| `{{.Texts}}` | []string | 待抽取源文 |
| `{{.Existing}}` | []string | 已有术语（去重） |

:::

### 术语精简变量

::: v-pre

| 变量 | 类型 | 说明 |
| --- | --- | --- |
| `{{.Glossary}}` | []GlossaryEntry | 当前术语表全量 |

:::

### 内置函数

| 函数 | 说明 |
| --- | --- |
| `mul` | `func(a float32, b int) float64`，术语密度等计算 |

### 用户消息协议

**JSON 模式（默认）** 用户消息示例：

```json
{
  "source_lang": "en",
  "target_lang": "zh",
  "segments": {
    "0": { "source": "Hello World", "translate": true },
    "1": { "source": "Context paragraph", "translate": false }
  }
}
```

期望回复：

```json
{ "translations": { "0": "你好世界" } }
```

**纯文本模式（TextMode=true）**：

```plaintext
[0] Hello World
[*] Context paragraph
```

期望回复：

```plaintext
[0] 你好世界
```

默认模板源码可参考：`backend/internal/templates/default/prompts/default.tmpl`。

---

## 执行配置

### 分段（split）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 是否分段 |
| `strategy` | string | `paragraph` | 当前仅 `paragraph` |
| `max_chars` | int | `1200` | 每段最大字符数 |

### 内容保护（protect）

不可译内容替换为 `__LF_NNNNNN__`，译后还原。

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 总开关 |
| `rules` | []string | `code`, `link`, `placeholder`, `xml` | 规则列表 |

| 规则 | 说明 |
| --- | --- |
| `code` | 行内/围栏代码 |
| `link` | URL 与 Markdown 链接 |
| `placeholder` | `{{var}}`、`%s` 等 |
| `xml` | HTML/XML 标签 |

### Ruby（ruby）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | 是否处理 `<ruby>` |
| `preserve_kinds` | []string | 视内置策略 | `phonetic` / `semantic` / `creative` |

| 分类 | 说明 |
| --- | --- |
| `phonetic` | 音注，通常保留不译 |
| `semantic` | 义训，通常保留不译 |
| `creative` | 创意注音，常需翻译 |

### 后处理（postprocess）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 总开关 |
| `trim_spaces` | bool | `true` | 裁剪多余空白 |

### 响应修复（repair）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 总开关 |
| `json_structural` | bool | `true` | JSON 结构修复 |
| `schema_aliases` | bool | `true` | 别名映射到 `translations` |
| `partial` | bool | `true` | 部分缺失时只重试缺失段 |
| `partial_threshold` | float | `0.5` | 缺失率阈值 |
| `placeholder_normalize` | bool | `true` | 占位符变体归一 |
| `prompt_upgrade` | bool | `true` | 失败时附加 reminder 重试 |

### 术语自举（bootstrap）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | 内联自举 |
| `max_terms_per_1000_chars` | float | `3.0` | 密度系数 |
| `min_source_len` | int | `2` | 源术语最短长度 |
| `inline_conflict_strategy` | string | `rewrite-local` | `off` \| `rewrite-local` |

### 质量检测（qa）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 总开关 |
| `length.enabled` | bool | `true` | 长度比检测 |
| `length.min_ratio` | float | `0.5` | 最小比 |
| `length.max_ratio` | float | `2.5` | 最大比 |
| `length.unit` | string | `char` | `char` \| `word` |
| `repetition.enabled` | bool | `true` | 相邻重复 |
| `untranslated.enabled` | bool | `true` | 译文=原文 |

源语残留（`source_residual`）按语言对自动启用，无单独开关；源语言为 `auto` 时不生效。审校侧说明见 [翻译审校](/zh/guide/review#质量检测)。

### 上下文（context）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | 总开关 |
| `before` | int | `1` | 前文章节数 |
| `after` | int | `1` | 后文章节数 |
| `max_chars` | int | `0` | 每段上下文上限，`0` 不限制 |

### 默认配置示例

```yaml
split:
  enabled: true
  strategy: paragraph
  max_chars: 1200

protect:
  enabled: true
  rules: [code, link, placeholder, xml]

ruby:
  enabled: true
  preserve_kinds: [creative]

postprocess:
  enabled: true
  trim_spaces: true

repair:
  enabled: true
  json_structural: true
  schema_aliases: true
  partial: true
  partial_threshold: 0.5
  placeholder_normalize: true
  prompt_upgrade: true

bootstrap:
  enabled: false
  max_terms_per_1000_chars: 3.0
  min_source_len: 2
  inline_conflict_strategy: "rewrite-local"

qa:
  enabled: true
  length:
    enabled: true
    min_ratio: 0.5
    max_ratio: 2.5
    unit: char
  repetition:
    enabled: true
  untranslated:
    enabled: true

context:
  enabled: true
  before: 1
  after: 1
  max_chars: 0
```

---

## 执行计划

### 轮次公共字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `mode` | string | `translate` / `extract` / `adjudicate` |
| `backend_id` | int | 后端 ID |
| `concurrency` | int | 并发（≥ 1） |
| `translate` | object | `mode=translate` 时必填 |
| `extract` | object | `mode=extract` 时必填 |
| `adjudicate` | object | `mode=adjudicate` 时必填 |

### extract

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `template_id` | int | — | 术语抽取模板 ID |
| `batch_size` | int | `20` | 每批段落上限；`0` 不限制 |
| `max_words_per_batch` | int | — | 每批字词上限 |
| `max_terms_per_1000_chars` | float | `25.0` | 抽取密度系数 |
| `min_source_len` | int | `2` | 术语最短源文 |
| `retry` | object | — | 重试 |

提取轮次只写术语表，不改段落译文。

### translate

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `prompt_template_id` | int | 翻译提示词模板 |
| `profile_id` | int | 执行配置 |
| `batch_size` | int | 每批段落上限，`0` 不限制 |
| `max_words_per_batch` | int | 每批字词上限，`0` 不限制 |
| `fallback_shrink` | float | 整批失败缩放 (0, 1) |
| `segment_filter` | object | `pending_only` / `skip_approved` / `all` 等 |
| `retry` | object | 重试 |

### adjudicate

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `batch_size` | int | — | 与 `max_words_per_batch` 至少填一项 |
| `max_words_per_batch` | int | — | 每批字词上限 |
| `adjudicate_codes` | []string | `["source_residual"]` | 仅 `source_residual` / `length_ratio` |
| `retry` | object | — | 重试 |

裁决提示词内置。`untranslated` / `duplicate` 不可裁决。

### 重试（retry）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `max_attempts` | int | `3` | 最大重试次数 |
| `backoff_ms` | int | `2000` | 基础退避毫秒 |
| `jitter` | bool | `true` | 随机抖动 |

### Ruby 重试（计划级）

| 字段 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `enabled` | bool | `false` | 本地还原失败时 LLM 对齐 |
| `backend_id` | int | `0` | `0` 表示用翻译主后端 |

### 校验摘要

- `rounds` 非空；每轮有合法 `mode` 与 `backend_id`
- 对应 mode 必须带齐子配置对象
- `batch_size` 与 `max_words_per_batch` 在翻译/裁决中不能同时为 0（提取两者皆 0 表示一次全量）
- `concurrency` ≥ 1；`fallback_shrink` ∈ [0, 1)

---

## 作用域

| 作用域 | 说明 |
| --- | --- |
| `system` | 内置，全局只读 |
| `user` | 创建者私有 |
| `org` | 组织共享（服务器模式 · 预览） |

## 相关文档

- [翻译配置 · 使用](/zh/guide/translation-config)
- [流水线与原理](/zh/guide/pipeline)
- [配置文件与环境变量](/zh/guide/configuration)（CLI YAML）
- [CLI 命令参考](/zh/guide/cli)
