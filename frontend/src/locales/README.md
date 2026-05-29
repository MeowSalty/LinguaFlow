# LinguaFlow 前端翻译贡献指南

前端多语言资源位于本目录，默认语言是简体中文 `zh-CN`。

## 新增语言

1. 复制 `zh-CN.ts`，并命名为目标 BCP 47 语言标签，例如 `en-US.ts`、`ja-JP.ts`。
2. 保持对象键结构与 `zh-CN.ts` 完全一致，只翻译字符串值。
3. 保留所有占位符名称，例如 `{name}`、`{url}`、`{count}`、`{percent}`、`{status}`。
4. 在 `index.ts` 中导入新语言文件，并把它加入 `localeOptions` 与 `messages`。
5. 运行前端类型检查和 lint，确保新增语言与中文基线结构一致。

## 键名规范

- 按功能域分组，例如 `login.form.username`、`dashboard.activity.empty`。
- 键名使用英文 camelCase，不使用中文、空格或标点。
- 同一功能的标题、按钮、校验、消息分别放在 `title`、`form`、`validation`、`messages` 等分组下。

## 占位符规则

- 翻译时必须保留原占位符名称和花括号。
- 可以根据目标语言语序移动占位符位置。
- 不要新增调用方未传入的占位符。

## 提交流程

1. Fork 仓库并创建翻译分支。
2. 新增或更新对应语言文件。
3. 更新 `index.ts` 中的语言元数据。
4. 运行：`pnpm --dir frontend type-check` 与 `pnpm --dir frontend lint`。
5. 提交 Pull Request，并说明目标语言、覆盖范围和是否有未确认翻译。

## 审核标准

- 语言文件键结构与 `zh-CN.ts` 保持一致。
- 用户界面没有明显机翻错误或缺失占位符。
- 不修改后端或 API 生成文件。
