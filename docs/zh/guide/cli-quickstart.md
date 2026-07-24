# 快速开始 · CLI

目标：不启动 Web，用命令行完成一次文件翻译。适合脚本、CI 或批量目录处理。

完整参数与子命令见 [CLI 命令参考](/zh/guide/cli)；配置文件字段见 [配置文件与环境变量](/zh/guide/configuration)。

## 前置条件

1. 已安装 `linguaflow` 可执行文件（[Releases](https://github.com/MeowSalty/LinguaFlow/releases) 或自行构建）
2. 可用的 AI API Key（OpenAI / Anthropic / Gemini，或兼容接口）

## 1. 生成配置

在工作目录执行：

```bash
linguaflow init
```

会生成：

```text
./
├── linguaflow.yaml
├── prompts/
│   ├── default_translation.tmpl
│   └── default_bootstrap.tmpl
└── profiles/
    └── default.yaml
```

## 2. 填入 API Key

编辑 `linguaflow.yaml`，至少保证：

1. `backends` 中有一个启用的后端，且 `options.api_key` 有效
2. `execution.rounds` 中有一轮 `mode: translate`，并引用该后端与默认提示词/策略

推荐用环境变量，避免把密钥写进文件：

```bash
export OPENAI_API_KEY=sk-...
```

配置中可写：

```yaml
backends:
  openai-default:
    type: openai
    enabled: true
    options:
      api_key: ${OPENAI_API_KEY}
      model: gpt-4o-mini
```

`linguaflow init` 生成的模板已包含类似结构，按注释改模型与密钥即可。

::: tip 非 OpenAI
将 `type` 改为 `anthropic` 或 `google`，并调整 `model` 与密钥环境变量。对接 Ollama 等时设置 `options.base_url`。
:::

## 3. 翻译单个文件

```bash
linguaflow translate -i README.md -o README_zh.md --to zh
```

## 4. 翻译整个目录

```bash
linguaflow translate -i ./docs -o ./docs-zh --to zh
```

不支持的扩展名会被跳过，结束时会输出成功 / 失败 / 跳过统计。

## 更多常用示例

::: code-group

```bash [指定源语言]
linguaflow translate -i article.md -o article.ja.md --from en --to ja
```

```bash [使用术语表]
linguaflow translate -i docs.md -o out.md --to zh --glossary-path ./terms.csv
```

```bash [指定配置文件]
linguaflow translate -c ./linguaflow.yaml -i in.md -o out.md --to zh
```

```bash [调试日志]
linguaflow translate -i in.md -o out.md --to zh -v
```

:::

## 成功标准

| #   | 标志                       | 如何确认                       |
| --- | -------------------------- | ------------------------------ |
| 1   | 进程退出码为 `0`           | 终端无报错退出                 |
| 2   | 输出路径出现译文文件       | `ls` / 资源管理器              |
| 3   | 打开文件可见译文           | 代码块、链接等尽量保持不被破坏 |
| 4   | （可选）目录批量有统计摘要 | 成功 / 失败 / 跳过数量         |

失败时加 `-v` 查看日志，并检查 API Key、网络与 `linguaflow.yaml` 中的后端配置。常见问题见 [FAQ](/zh/guide/faq)。

## 与 Web 的关系

|          | CLI `translate`      | Web 本地模式                      |
| -------- | -------------------- | --------------------------------- |
| 入口     | 命令行，无界面       | `linguaflow` / `linguaflow local` |
| 配置     | `linguaflow.yaml` 等 | 界面内后端 / 计划 / 项目          |
| 审校     | 需自行编辑输出文件   | 段落审校、状态、质检              |
| 典型用途 | 批处理、脚本、CI     | 交互翻译与质检                    |

两者能力有重叠，但 **质量裁决（adjudicate）等部分能力以 Web 执行计划为主**。详见 [翻译配置 · 使用](/zh/guide/translation-config)。

## 下一步

- [CLI 命令参考](/zh/guide/cli) — 全部子命令与参数
- [配置文件与环境变量](/zh/guide/configuration) — 配置文件结构说明
- [快速开始 · Web](/zh/guide/getting-started) — 可视化流程
- [核心概念](/zh/guide/concepts) — 术语对照
