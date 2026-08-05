# 翻译配置 · 参考

本页为 **检索型参考**：AI 后端选项、提示词变量、执行配置字段、执行计划轮次字段与默认值。

::: tip 使用说明
界面步骤与场景见 [翻译配置 · 使用](/zh/guide/translation-config)。行为原理见 [流水线与原理](/zh/guide/pipeline)。CLI 的 `linguaflow.yaml` 结构见 [配置文件与环境变量](/zh/guide/configuration)。
:::

## AI 后端

### 类型与示例模型

| 后端          | 类型标识    | 示例模型            | 说明                                          |
| ------------- | ----------- | ------------------- | --------------------------------------------- |
| OpenAI        | `openai`    | `gpt-4o-mini`       | 兼容 Azure / Ollama / LM Studio 等 OpenAI API |
| Anthropic     | `anthropic` | `claude-sonnet-4-5` | Tool Use 结构化输出；可选提示缓存             |
| Google Gemini | `google`    | `gemini-2.5-flash`  | ResponseMIMEType 结构化输出                   |

::: info 探测模型
Web 可用「探测模型」拉取列表后选择。
:::

### 通用 options

| 选项                    | 类型       | 默认值                                | 说明                                                  |
| ----------------------- | ---------- | ------------------------------------- | ----------------------------------------------------- |
| `api_key`               | string     | **必填**                              | API 密钥，支持 `${ENV_VAR}`                           |
| `base_url`              | string     | SDK 默认                              | 自定义端点                                            |
| `model`                 | string     | **必填**                              | 模型 ID                                               |
| `max_tokens`            | int        | OpenAI: `0`；Anthropic/Gemini: `8192` | 最大生成 token；`0` 常表示不额外限制                  |
| `timeout`               | int/string | `60`（秒）                            | 秒数或 Go duration；Web 上可关闭超时（关闭后实际请求不设时限） |
| `response_format`       | string     | `json_schema`                         | `json_schema` \| `json_object` \| `text` \| `none`    |
| `temperature`           | float      | API 默认                              | 采样温度                                              |
| `top_p`                 | float      | API 默认                              | 核采样                                                |
| `stream`                | bool       | `false`                               | 上游流式请求，内部累积为完整响应                      |
| `thinking_level`        | string     | `off`                                 | 统一思考强度：`off` \| `low` \| `medium` \| `high`    |
| `rate_limit_per_minute` | int        | `0`                                   | 每分钟请求上限；`0` 不限（后端级字段，非 options 内） |

### 思考强度（`thinking_level`）

统一语义，由各适配层映射为厂商原生参数；仅对支持推理/思考的模型生效。

| 档位                      | 含义                                                                                                                 |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `off`（默认）             | LinguaFlow **不传**任何 thinking 相关字段，沿用模型/网关默认。默认会思考的推理模型在 `off` 下仍可能思考（by design） |
| `low` / `medium` / `high` | 显式开启并按档位映射                                                                                                 |

| 后端          | 开启时映射                                       | 注意                                                                                                                                                |
| ------------- | ------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| OpenAI 兼容   | `reasoning_effort`（字面 `low`/`medium`/`high`） | 不支持的模型可能忽略或报错                                                                                                                          |
| Anthropic     | `thinking` + `budget_tokens`                     | 开启后 **忽略** temperature / top_p；budget 与最终输出 **共用** `max_tokens`（约 low 25% / medium 50% / high 75% 给思考）；要求 `max_tokens > 1024` |
| Google Gemini | `ThinkingConfig.ThinkingLevel`                   | 不设 budget / include thoughts                                                                                                                      |

截断（如 Anthropic `stop_reason=max_tokens`）且已开思考时，可从 **提高 `max_tokens`、降低 `thinking_level`、减小批次** 三处排查。

### Anthropic 专有

| 选项                  | 类型 | 默认值 | 说明                                    |
| --------------------- | ---- | ------ | --------------------------------------- |
| `enable_prompt_cache` | bool | `true` | system prompt 缓存断点，降低 token 消耗 |

### 探测可用模型（Web / API）

`POST /api/v1/backends/models`：用当场填写的 `type`、`api_key` 与可选 `base_url` 向服务商拉取模型列表，**凭据不落库**。返回 `items[].id` / `name`，可将 `id` 直接写入 `options.model`。

### Base URL 示例

| 服务         | 示例                                                             |
| ------------ | ---------------------------------------------------------------- |
| Azure OpenAI | `https://<resource>.openai.azure.com/openai/deployments/<model>` |
| Ollama       | `http://localhost:11434/v1`                                      |
| LM Studio    | `http://localhost:1234/v1`                                       |

---

## 提示词模板

### 类型

| 类型                  | 用途                   |
| --------------------- | ---------------------- |
| 翻译提示词            | 翻译阶段 system prompt |
| 术语抽取（Bootstrap） | 提取术语阶段           |
| 术语精简（Prune）     | 术语表清理阶段         |

Web 中在对应资源页管理；内置模板 scope 为 `system`，不可改删。

### 翻译提示词变量

::: v-pre

| 变量                     | 类型            | 说明                                     |
| ------------------------ | --------------- | ---------------------------------------- |
| `{{.SourceLang}}`        | string          | 源语言，`auto` 为自动检测                |
| `{{.TargetLang}}`        | string          | 目标语言（BCP 47）                       |
| `{{.Source}}`            | string          | 单段模式源文本                           |
| `{{.Segments}}`          | []SegmentInput  | 批量段落：`ID` / `Source` / `Translate`  |
| `{{.Glossary}}`          | []GlossaryEntry | 术语：`Source` / `Target` / `Notes`      |
| `{{.TMHints}}`           | []TMHint        | 翻译记忆：`Source` / `Target` / `Score`  |
| `{{.TextMode}}`          | bool            | `true` 纯文本编号；`false` JSON envelope |
| `{{.StrictSchema}}`      | bool            | 后端 json_schema 强制时精简协议描述      |
| `{{.InlineBootstrap}}`   | bool            | 是否内联抽术语                           |
| `{{.MaxBootstrapTerms}}` | int             | 内联术语上限                             |
| `{{.HasRuby}}`           | bool            | 是否有 Ruby 注音                         |
| `{{.RubyMode}}`          | string          | `json` \| `section` \| `inline`          |
| `{{.RubyAnnotations}}`   | map             | 段落 ID → 注音列表                       |

:::

### 术语抽取变量

::: v-pre

| 变量              | 类型     | 说明             |
| ----------------- | -------- | ---------------- |
| `{{.SourceLang}}` | string   | 源语言           |
| `{{.TargetLang}}` | string   | 目标语言         |
| `{{.MaxTerms}}`   | int      | 最多抽取条数     |
| `{{.Texts}}`      | []string | 待抽取源文       |
| `{{.Existing}}`   | []string | 已有术语（去重） |

:::

### 术语精简变量

::: v-pre

| 变量            | 类型            | 说明           |
| --------------- | --------------- | -------------- |
| `{{.Glossary}}` | []GlossaryEntry | 当前术语表全量 |

:::

### 内置函数

| 函数  | 说明                                             |
| ----- | ------------------------------------------------ |
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

| 字段        | 类型   | 默认值      | 说明               |
| ----------- | ------ | ----------- | ------------------ |
| `enabled`   | bool   | `true`      | 是否分段           |
| `strategy`  | string | `paragraph` | 当前仅 `paragraph` |
| `max_chars` | int    | `1200`      | 每段最大字符数     |

### 内容保护（protect）

不可译内容替换为 `__LF_NNNNNN__`，译后还原。

| 字段      | 类型     | 默认值                               | 说明     |
| --------- | -------- | ------------------------------------ | -------- |
| `enabled` | bool     | `true`                               | 总开关   |
| `rules`   | []string | `code`, `link`, `placeholder`, `xml` | 规则列表 |

| 规则          | 说明                 |
| ------------- | -------------------- |
| `code`        | 行内/围栏代码        |
| `link`        | URL 与 Markdown 链接 |
| `placeholder` | `{{var}}`、`%s` 等   |
| `xml`         | HTML/XML 标签        |

### Ruby（ruby）

| 字段             | 类型     | 默认值     | 说明                                 |
| ---------------- | -------- | ---------- | ------------------------------------ |
| `enabled`        | bool     | `false`    | 是否处理 `<ruby>`                    |
| `preserve_kinds` | []string | 视内置策略 | `phonetic` / `semantic` / `creative` |

| 分类       | 说明               |
| ---------- | ------------------ |
| `phonetic` | 音注，通常保留不译 |
| `semantic` | 义训，通常保留不译 |
| `creative` | 创意注音，常需翻译 |

### 后处理（postprocess）

| 字段          | 类型 | 默认值 | 说明         |
| ------------- | ---- | ------ | ------------ |
| `enabled`     | bool | `true` | 总开关       |
| `trim_spaces` | bool | `true` | 裁剪多余空白 |

### 响应修复（repair）

| 字段                    | 类型 | 默认值 | 说明                      |
| ----------------------- | ---- | ------ | ------------------------- |
| `enabled`               | bool | `true` | 总开关                    |
| `json_structural`       | bool | `true` | JSON 结构修复             |
| `schema_aliases`        | bool | `true` | 别名映射到 `translations` |
| `placeholder_normalize` | bool | `true` | 占位符变体归一            |
| `prompt_upgrade`        | bool | `true` | 失败时附加 reminder 重试  |

::: tip 部分段缺失由池化重试兜底
修复层只负责把模型返回解析成「可解析的翻译 ID 列表」；个别段没回怎么办，由 [流水线的池化缩批重试](/zh/guide/pipeline#批量与并发) 统一处理（按 `fallback_shrink` 缩小批次只重译缺失段），不再在修复配置里单独开关。
:::

### 术语自举（bootstrap）

| 字段                       | 类型   | 默认值          | 说明                     |
| -------------------------- | ------ | --------------- | ------------------------ |
| `enabled`                  | bool   | `false`         | 内联自举                 |
| `max_terms_per_1000_chars` | float  | `3.0`           | 密度系数                 |
| `min_source_len`           | int    | `2`             | 源术语最短长度           |
| `inline_conflict_strategy` | string | `rewrite-local` | `off` \| `rewrite-local` |

### 质量检测（qa）

| 字段                   | 类型     | 默认值 | 说明                                                                                       |
| ---------------------- | -------- | ------ | ------------------------------------------------------------------------------------------ |
| `enabled`              | bool     | `true` | 总开关                                                                                     |
| `checks`               | []string | `nil`  | 启用的确定性 checker 名称；`nil`/缺省 = 启用全部；空数组会被视为「等价于全部」并改回 `nil` |
| `length.enabled`       | bool     | `true` | 长度比检测（与 `checks` 中的 `length_ratio` 名等价）                                       |
| `length.min_ratio`     | float    | `0.5`  | 最小比                                                                                     |
| `length.max_ratio`     | float    | `2.5`  | 最大比                                                                                     |
| `length.unit`          | string   | `char` | `char` \| `word`                                                                           |
| `repetition.enabled`   | bool     | `true` | 相邻重复（与 `checks` 中的 `duplicate` 名等价）                                            |
| `untranslated.enabled` | bool     | `true` | 译文=原文（与 `checks` 中的 `untranslated` 名等价）                                        |

#### 可配置 checker 名称（`qa.checks`）

`checks` 接受下列 `Checker.Name()` 取值。`nil` 表示启用全部 15 项 per-batch checker；非 `nil` 时只运行名单中的 checker（精确匹配）。文档级 `duplicate_source_divergence` 始终随引擎运行，不能也不必在此排除。

| 名称                          | 说明                                           |
| ----------------------------- | ---------------------------------------------- |
| `untranslated`                | 译文=原文                                       |
| `length_ratio`                | 长度过短/过长                                   |
| `duplicate`                   | 相邻译文相同                                    |
| `source_residual`             | 源语脚本残留（按语言对分档）                    |
| `punctuation_pairing`         | 标点配对不平衡                                  |
| `whitespace_irregular`        | 零宽/NBSP/制表符等异常空白                     |
| `repeated_space`              | 连续空格 / CJK 间空格                          |
| `width_mix`                   | 全/半角混用                                    |
| `number_mismatch`             | 阿拉伯数字集合不一致                           |
| `url_email_mismatch`          | URL/邮箱集合不一致                             |
| `subtitle_line_count`        | 字幕行数不一致                                 |
| `forbidden_term`              | 命中禁译词条仍出现                             |
| `term_inconsistency`          | 命中强制词条未用 target                        |
| `leftover_placeholder`        | 译文残留占位符                                 |
| `xml_tag_mismatch`           | XML 标签集合不一致                             |

源语残留（`source_residual`）随质量检测引擎自动启用，无单独开关；源语言为 `auto` 时不生效。审校侧说明见 [翻译审校](/zh/guide/review#质量检测)。

### 上下文（context）

| 字段        | 类型 | 默认值 | 说明                                                      |
| ----------- | ---- | ------ | --------------------------------------------------------- |
| `enabled`   | bool | `true` | 总开关                                                    |
| `before`    | int  | `1`    | 前文章节数                                                |
| `after`     | int  | `1`    | 后文章节数                                                |
| `max_chars` | int  | `0`    | 每段上下文上限（按 rune 计，超限截断并补省略号）；`0` 不限制 |

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
  placeholder_normalize: true
  prompt_upgrade: true

bootstrap:
  enabled: false
  max_terms_per_1000_chars: 3.0
  min_source_len: 2
  inline_conflict_strategy: "rewrite-local"

qa:
  enabled: true
  # checks:               # 留空（缺省）表示启用全部确定性 checker
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

| 字段           | 类型   | 说明                                            |
| -------------- | ------ | ----------------------------------------------- |
| `mode`         | string | `translate` / `extract` / `adjudicate` / `semantic_qa` |
| `backend_id`   | int    | 后端 ID                                         |
| `concurrency`  | int    | 并发（≥ 1）                                     |
| `translate`    | object | `mode=translate` 时必填                         |
| `extract`      | object | `mode=extract` 时必填                           |
| `adjudicate`   | object | `mode=adjudicate` 时必填                        |
| `semantic_qa`  | object | `mode=semantic_qa` 时必填                       |

### extract

| 字段                       | 类型   | 默认值 | 说明                     |
| -------------------------- | ------ | ------ | ------------------------ |
| `template_id`              | int    | —      | 术语抽取模板 ID          |
| `batch_size`               | int    | `20`   | 每批段落上限；`0` 不限制 |
| `max_words_per_batch`      | int    | —      | 每批字词上限             |
| `max_terms_per_1000_chars` | float  | `25.0` | 抽取密度系数             |
| `min_source_len`           | int    | `2`    | 术语最短源文             |
| `retry`                    | object | —      | 重试                     |

提取轮次只写术语表，不改段落译文。

### translate

| 字段                  | 类型   | 说明                                                                                                                       |
| --------------------- | ------ | -------------------------------------------------------------------------------------------------------------------------- |
| `prompt_template_id`  | int    | 翻译提示词模板                                                                                                             |
| `profile_id`          | int    | 执行配置                                                                                                                   |
| `batch_size`          | int    | 待译段落数上限（**不计上下文段**）；`0` 不限制，与 `max_words_per_batch` 至少填一项                                        |
| `max_words_per_batch` | int    | 字词数上限（**计入上下文段**）；`0` 不限制，与 `batch_size` 至少填一项。纯行数模式（此项与 `context.max_chars` 均为 0）下上下文体积不受约束 |
| `fallback_shrink`     | float  | 池缩放系数 (0, 1)；`0`/省略 = 单池不缩批。池 N 批次约束 = `floor(原始 × shrink^N)`，各池串行，失败批次进下一更小的池 |
| `segment_filter`      | object | `pending_only` / `skip_approved` / `all` 等                                                                                |
| `retry`               | object | 重试                                                                                                                       |

::: tip 上下文与批次约束的关系
- `batch_size` 只数「待译段」：开 2 段上下文、`batch_size=10` 时，实际送模型的段落最多是 10 段待译 + 前后各 1 段上下文。
- `max_words_per_batch` 则把上下文段的字数也**算进预算**，避免「上下文把整批塞满」导致超限。为防止预估偏差，流水线还会在组批时对上下文段做词数预估并扣减预算。
- `context.max_chars` 与 `max_words_per_batch` **都为 0**（纯行数模式）时，上下文体积完全不受约束，仅在确信上下文很短时使用。
:::

### adjudicate

| 字段                  | 类型     | 默认值                | 说明                                  |
| --------------------- | -------- | --------------------- | ------------------------------------- |
| `batch_size`          | int      | —                     | 与 `max_words_per_batch` 至少填一项   |
| `max_words_per_batch` | int      | —                     | 每批字词上限                          |
| `adjudicate_codes`    | []string | `["source_residual"]` | 仅 `source_residual` / `length_ratio` |
| `retry`               | object   | —                     | 重试                                  |

裁决提示词内置。`untranslated` / `duplicate` 不可裁决。

输入/输出协议随后端 `response_format` 自动切换：

| 协议                 | 何时             | 用户消息                               | 模型回复                                                      |
| -------------------- | ---------------- | -------------------------------------- | ------------------------------------------------------------- |
| JSON（strict/loose） | 默认 / 非 text   | JSON envelope（`segments` + `issues`） | `{"verdicts":[...]}`                                          |
| text                 | 后端为纯文本模式 | 纯文本 `[segment]` 块                  | `[verdicts]` 段，每行 `id \| issue_code \| verdict \| reason` |

text 模式下若模型仍输出 JSON，解析会自动降级为 JSON，无需重试。

### semantic_qa

语义质检轮次配置。系统提示词内置不可见，无 `prompt_template_id`；产出的语义类问题以 `warning` 直接进人审。

| 字段                  | 类型     | 默认值 | 说明                                                                                                                  |
| --------------------- | -------- | ------ | --------------------------------------------------------------------------------------------------------------------- |
| `batch_size`          | int      | —      | 段落数上限；`0` 不限制，与 `max_words_per_batch` 至少填一项                                                           |
| `max_words_per_batch` | int      | —      | 字词数上限；`0` 不限制，与 `batch_size` 至少填一项                                                                    |
| `segment_scope`       | string   | `all`  | 段落扫描范围：`all` / `with_issues` / `with_issue_codes`                                                              |
| `issue_codes`         | []string | —      | 仅 `segment_scope=with_issue_codes` 时生效，须 ≥ 1 项；取值见下方                                                       |
| `retry`               | object   | —      | 重试                                                                                                                  |

#### `segment_scope` 取值

| 值                  | 扫描范围                                                                                |
| ------------------- | --------------------------------------------------------------------------------------- |
| `all`（默认）       | 全部 `translated` / `edited` 且译文非空的段，完整语义覆盖                                |
| `with_issues`       | 仅扫描带任意 issue 的段                                                                 |
| `with_issue_codes`  | 仅扫描含 `issue_codes` 声明 code 的段（用于成本敏感的高价值子集，如 ja↔zh 假同源检测） |

范围与任务级 `segment_ids` 取交集。`scope ≠ all` 时未扫到的段会保留其原有的语义 issue。`scope=with_issue_codes` 时必须选至少一个 code。

#### `issue_codes` 取值

支持 7 个 code，规则码与语义码都可作筛选键：

`source_residual`、`length_ratio`、`untranslated`、`duplicate`、`calque`、`term_fidelity`、`naturalness`

#### 软警告

扫描轮失败不视为终态失败：失败原因写入资源 `warning_message`，作业继续。作业进度卡片与资源详情页会提示该资源质检不完整，需人工补查。

### 重试（retry）

| 字段           | 类型 | 默认值 | 说明                                                                                                       |
| -------------- | ---- | ------ | ---------------------------------------------------------------------------------------------------------- |
| `max_attempts` | int  | `3`    | 双重角色：每池在途重试预算（1 次首调 + N 次重试）；启用 `fallback_shrink` 时还决定池数量（池数 = `max_attempts + 1`） |
| `backoff_ms`   | int  | `2000` | 基础退避毫秒                                                                                               |
| `jitter`       | bool | `true` | 随机抖动                                                                                                   |

::: tip `max_attempts` 与池化缩批的关系
启用 `fallback_shrink` 后，`max_attempts` 同时控制「池深度」：池数量 = `max_attempts + 1`，池 N 的批次约束 = `floor(原始 × shrink^N)`。最坏情况下单段调用次数 ≈ `(max_attempts + 1)²`（每池重试 × 池数）。机制细节见 [流水线与原理 · 批量与并发](/zh/guide/pipeline#批量与并发)。
:::

### Ruby 重试（计划级）

| 字段         | 类型 | 默认值  | 说明                    |
| ------------ | ---- | ------- | ----------------------- |
| `enabled`    | bool | `false` | 本地还原失败时 LLM 对齐 |
| `backend_id` | int  | `0`     | `0` 表示用翻译主后端    |

### 校验摘要

- `rounds` 非空；每轮有合法 `mode` 与 `backend_id`
- 对应 mode 必须带齐子配置对象
- `batch_size` 与 `max_words_per_batch` 在翻译/裁决/语义质检中不能同时为 0（提取两者皆 0 表示一次全量）
- `semantic_qa.segment_scope=with_issue_codes` 时必须提供 ≥ 1 个 `issue_codes`
- `concurrency` ≥ 1；`fallback_shrink` ∈ (0, 1) 或为 0（禁用池化缩批）

---

## 作用域

| 作用域   | 说明                          |
| -------- | ----------------------------- |
| `system` | 内置，全局只读                |
| `user`   | 创建者私有                    |
| `org`    | 组织共享（服务器模式 · 预览） |

---

## 词条属性（forbidden / mandatory）

词条（GlossaryEntry）除原有的 `source` / `target` / `case_sensitive` / `notes` 外，新增两个布尔属性：

| 字段          | 类型   | 默认值  | 说明                                                                                              |
| ------------- | ------ | ------- | ------------------------------------------------------------------------------------------------- |
| `forbidden`   | bool   | `false` | 是否为禁译条目；命中源词且译文包含 `target` 时产生 `forbidden_term` error                          |
| `mandatory`   | bool   | `true`  | 是否为强制条目；命中源词时强制译文使用 `target`，否则产生 `term_inconsistency` warning             |

唯一索引按 `forbidden` 取值分两种：

- `forbidden=false` → `(project_id, source_key)` 唯一，同源词只能一条
- `forbidden=true` → `(project_id, source_key, target)` 唯一，允许同源词多条不同禁译目标

创建 / 更新请求（`CreateGlossaryEntryRequest` / `UpdateGlossaryEntryRequest`）默认 `forbidden=false`、`mandatory=true`。产品侧说明见 [术语表管理 · 禁译与强制](/zh/guide/glossary#禁译-forbidden-与强制-mandatory)。

---

## 单段试译（preview）

在不创建作业的前提下，对单段原文用某执行计划在内存中跑一遍流水线，预览译文与诊断信息后决定是否应用。

### `POST /projects/{projectId}/resources/{resourceId}/segments/{segmentId}/translation-preview`

| 字段                | 类型 | 必填 | 说明                                                                                       |
| ------------------- | ---- | ---- | ------------------------------------------------------------------------------------------ |
| `execution_plan_id` | int  | 是   | 执行计划 ID；计划必须至少包含一个 `translate` 轮次                                          |
| `source_text`       | string | 否  | 可选：临时覆盖原文（模拟原文变更后翻译）；省略时使用数据库原文，传入时必须为非空白文本     |

响应（`SegmentTranslationPreviewResponse`）关键字段：

| 字段                | 说明                                                                                       |
| ------------------- | ------------------------------------------------------------------------------------------ |
| `status`            | `success` / `partial` / `failed`（终态译文状态）                                            |
| `target_text`       | 最终译文；`failed` 时可能为空                                                              |
| `quality_issues`    | 汇总质量问题（规则 QA + adjudicate + semantic QA）                                          |
| `execution.rounds`  | 各轮执行摘要（index / mode），不暴露 backend options                                        |
| `usage`             | 用量统计（与正式作业区分）                                                                  |
| `batches`           | 批次诊断（`TranslationBatchDiagnostic`）：含 `round_index` / `attempt` / `system_prompt` / `user_message` / `response_format` / `json_schema` / `response` 等 |

预览在内存沙箱执行：术语表用 overlay、翻译记忆用 Noop，不持久化。

### `POST .../segments/{segmentId}/translation-preview/apply`

| 字段          | 类型   | 必填 | 说明                                          |
| ------------- | ------ | ---- | --------------------------------------------- |
| `apply_token` | string | 是   | 预览返回的签名令牌（带过期时间）              |
| `target_text` | string | 是   | 预览后（可能被人工修改）的译文；为空返回 400  |

应用时做基线条件更新（source / target / status 必须与预览时一致）；段落已变更则令牌失效，需重新试译。

---

## 相关文档

- [翻译配置 · 使用](/zh/guide/translation-config)
- [流水线与原理](/zh/guide/pipeline)
- [配置文件与环境变量](/zh/guide/configuration)（CLI YAML）
- [CLI 命令参考](/zh/guide/cli)
