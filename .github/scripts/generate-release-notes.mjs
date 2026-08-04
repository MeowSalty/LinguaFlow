#!/usr/bin/env node
/**
 * 生成 GitHub Release Notes（AI 概述 + 折叠的完整提交列表）。
 *
 * CI 用法（由 release.yml 调用）：
 *   依赖 GITHUB_REF（refs/tags/v*）与 AI_* 环境变量。
 *
 * 本地调试（PowerShell）：
 *   $env:AI_BASE_URL="http://axonhub.home.server/v1"
 *   $env:AI_API_KEY="ah-xxx"
 *   $env:AI_MODEL="deepseek-v4-pro"
 *   # 可选：不设则自动用最近两个 tag
 *   $env:VERSION="0.2.0"
 *   $env:PREV_VERSION="v0.1.0"
 *   node .github/scripts/generate-release-notes.mjs
 *
 * 输出：release-notes.md
 */

import fs from 'node:fs';
import { execSync } from 'node:child_process';

const OUTPUT = process.env.OUTPUT_PATH || 'release-notes.md';
const baseUrl = (process.env.AI_BASE_URL || 'https://api.openai.com/v1').replace(/\/$/, '');
const model = process.env.AI_MODEL || 'gpt-4o';

function git(cmd) {
  return execSync(cmd, { encoding: 'utf8' }).trim();
}

/** 将 git shortstat 转为中文描述 */
function formatStats(shortstat) {
  const files = Number((shortstat.match(/(\d+) files? changed/) || [])[1] || 0);
  const insertions = Number((shortstat.match(/(\d+) insertions?/) || [])[1] || 0);
  const deletions = Number((shortstat.match(/(\d+) deletions?/) || [])[1] || 0);
  const n = (x) => x.toLocaleString('en-US');
  const parts = [`${n(files)} 个文件变更`];
  if (insertions) parts.push(`${n(insertions)} 行新增代码`);
  if (deletions) parts.push(`${n(deletions)} 行删除代码`);
  return parts.join('，');
}

/** 解析版本范围：优先 env，其次 GITHUB_REF，再次最近两个 tag */
function resolveRange() {
  let version = process.env.VERSION;
  let prevVersion = process.env.PREV_VERSION;
  let currentTag = process.env.CURRENT_TAG;

  if (!version && process.env.GITHUB_REF?.startsWith('refs/tags/')) {
    currentTag = process.env.GITHUB_REF.replace(/^refs\/tags\//, '');
    version = currentTag.replace(/^v/, '');
  }

  if (!currentTag && version) {
    currentTag = `v${version}`;
  }

  if (!prevVersion) {
    const tags = git('git tag --sort=-creatordate')
      .split('\n')
      .map((t) => t.trim())
      .filter(Boolean);
    if (currentTag) {
      prevVersion = tags.find((t) => t !== currentTag) || '';
    } else if (tags.length >= 2) {
      currentTag = tags[0];
      version = currentTag.replace(/^v/, '');
      prevVersion = tags[1];
    } else if (tags.length === 1) {
      currentTag = tags[0];
      version = currentTag.replace(/^v/, '');
      prevVersion = '';
    }
  }

  if (!version) {
    throw new Error('无法确定版本：请设置 VERSION / PREV_VERSION，或在带 tag 的仓库中运行');
  }

  return { version, prevVersion: prevVersion || '', currentTag: currentTag || `v${version}` };
}

function collectHistory(version, prevVersion, currentTag) {
  // CI 推 tag 时 HEAD 即当前版本；本地调试用 v${version} / currentTag 作为右端点
  const end = process.env.GITHUB_REF ? 'HEAD' : currentTag || `v${version}`;

  if (!prevVersion) {
    const emptyTree = git('git hash-object -t tree /dev/null');
    return {
      prevVersion: '初始版本',
      commitCount: git(`git rev-list --count ${end}`),
      stats: formatStats(git(`git diff --shortstat ${emptyTree} ${end}`)),
      commits: git(`git log ${end} --pretty=format:"- %s" --no-merges`),
    };
  }

  const range = `${prevVersion}..${end}`;
  return {
    prevVersion,
    commitCount: git(`git rev-list --count ${range}`),
    stats: formatStats(git(`git diff --shortstat ${prevVersion} ${end}`)),
    commits: git(`git log ${range} --pretty=format:"- %s" --no-merges`),
  };
}

/** 按 type 分组生成完整提交列表（不消耗 AI token） */
function buildFullChangelog(commitsText) {
  const lines = commitsText.split('\n').filter(Boolean);
  const groups = {
    feat: [],
    fix: [],
    refactor: [],
    perf: [],
    docs: [],
    style: [],
    test: [],
    build: [],
    ci: [],
    chore: [],
    other: [],
  };
  const breaking = [];

  for (const line of lines) {
    const m = line.match(/^- (\w+)(\([^)]*\))?(!)?:\s*(.*)$/);
    if (!m) {
      groups.other.push(line);
      continue;
    }
    const [, type, scope, bang, subject] = m;
    const prefix = scope ? `**${scope.slice(1, -1)}**: ` : '';
    if (bang) {
      breaking.push(`- ${prefix}${subject} *(破坏性)*`);
      continue;
    }
    const key = groups[type] ? type : 'other';
    groups[key].push(`- ${prefix}${subject}`);
  }

  const sections = [
    { key: 'feat', title: '✨ 新功能' },
    { key: 'fix', title: '🐛 问题修复' },
    { key: 'refactor', title: '♻️ 重构' },
    { key: 'perf', title: '⚡ 性能优化' },
    { key: 'docs', title: '📝 文档' },
    { key: 'style', title: '🎨 样式' },
    { key: 'test', title: '🧪 测试' },
    { key: 'build', title: '📦 构建' },
    { key: 'ci', title: '🔧 CI' },
    { key: 'chore', title: '🔀 杂项' },
    { key: 'other', title: '📋 其他' },
  ];

  let out = '';
  if (breaking.length) {
    out += '### ⚠️ 破坏性变更\n\n' + breaking.join('\n') + '\n\n';
  }
  for (const s of sections) {
    if (groups[s.key].length === 0) continue;
    out += `### ${s.title}\n\n` + groups[s.key].join('\n') + '\n\n';
  }
  return out.trim();
}

function buildPrompt(version, prevVersion, commitCount, commits, stats) {
  return `你是一位专业的开源项目维护者，正在为 LinguaFlow（AI 驱动的多语言翻译平台）撰写 v${version} 的 GitHub Release 说明。

以下是自 ${prevVersion} 以来的 ${commitCount} 条提交记录（约定式提交格式）：

${commits}

变更统计：${stats}

请基于这些提交生成 Markdown 格式的 Release Notes **概述部分**（不要罗列全部提交，完整提交列表会由脚本另行追加）。

要求：

1. 功能聚合：将散落在多个 scope 中的同一业务功能（如"术语精简"、"翻译质量检测"）合并为一个章节，每个章节先用一句话总结功能价值，再用要点形式列出该功能的关键变化（合并语义重复的提交）。

2. 章节结构（按需输出，无内容的章节请省略）：
   - 🎉 版本简介（2-3 句话概括本版本主题；若有破坏性变更，在简介正文末尾紧接 > [!WARNING] 摘要，见第 5 条）
   - 🚀 新功能（按业务模块聚合，每个模块用三级标题分隔）
   - 🐛 问题修复（按问题维度归纳，不逐条罗列）
   - ⚠️ 破坏性变更（详细说明 + 升级迁移清单）
   - ♻️ 重构与优化（按主题归纳）

3. GitHub Flavored Markdown 语法规范（主动使用，输出将在 GitHub Release 页面渲染）：

   基础语法：
   - 标题层级：文档用二级标题（##），模块用三级标题（###），不要使用一级标题（#）。
   - 列表：要点用 - 无序列表，有序步骤用 1. 2. 3.。
   - 粗体：**强调**关键术语（实体名、配置名、按钮名等）。
   - 行内代码：用反引号包裹配置字段、API 名、命令、文件路径，如 \`output_format\`、\`go build\`。
   - 链接：引用外部资源时用 [文本](URL) 格式。
   - 表格：对比性内容（如新旧字段对照、迁移前后变化）优先用表格呈现，而非列表。
   - 删除线：废弃的旧字段/旧命令用 ~~旧名~~ 标注，便于用户识别弃用项。

   高级语法（GitHub 特有，务必主动使用）：
   - Admonitions 警告框：用于突出破坏性变更、升级提示、重要配置。
     格式为以 > [!TYPE] 开头的引用块，支持类型：NOTE / TIP / IMPORTANT / WARNING / CAUTION。
     示例：
       > [!WARNING]
       > 本版本包含破坏性变更，升级前请阅读：
       >
       > - 变更点 1
       > - 变更点 2

   使用建议：
     - 破坏性变更摘要用 > [!WARNING] 包裹，放在「版本简介」正文末尾（仅摘要，不放完整迁移步骤）
     - 升级迁移步骤用 > [!IMPORTANT] 包裹，放在「⚠️ 破坏性变更」章节内
     - 新功能的使用建议、小技巧用 > [!TIP] 包裹
     - 普通说明性备注用 > [!NOTE] 包裹
     - 不要滥用，仅在真正需要读者注意时使用，每个类型整篇不超过 2 处

   - 任务列表：若描述迁移步骤或检查清单，用 - [ ] / - [x] 渲染为复选框。
   - 键盘按键：交互说明中用 <kbd> 包裹按键名，如 <kbd>Ctrl</kbd> + <kbd>S</kbd>、<kbd>Enter</kbd>。
   - Emoji：可在章节标题前使用 emoji 增强可读性（已在章节结构中指定），正文避免堆砌。

4. 措辞要求：
   - 使用中文
   - 去除提交消息中的 type/scope 前缀（如 feat、feat(glossary)、fix 等前缀），保留实质内容
   - 隐藏实现细节，突出用户可感知的变化
   - 不要包含提交哈希、PR 编号、作者列表
   - 不要输出"完整提交列表"或"变更统计"章节（由脚本追加）

5. 破坏性变更识别与放置：约定式提交中带感叹号的（如 refactor!: 或 refactor(openapi)!:）必须纳入破坏性变更。有破坏性变更时分两处输出：
   - 「🎉 版本简介」正文之后：用 > [!WARNING] 列出变更要点摘要（合并语义重复项，用 - 列表），便于读者开篇即见
   - 「## ⚠️ 破坏性变更」章节：可补充说明，并用 > [!IMPORTANT] 给出升级迁移检查清单（- [ ] 任务列表）
   不要在简介里重复完整迁移步骤；WARNING 只放变更摘要，IMPORTANT 只放在破坏性变更章节。

6. 直接输出 Markdown 正文，不要包含 markdown 代码块包裹，不要以任何标题前的分隔符开头。

请生成 Release Notes 概述部分：`;
}

function buildFinalContent(aiContent, commits, commitCount, stats) {
  const fullChangelog = buildFullChangelog(commits);
  const foldedChangelog = `<details>\n<summary>📖 完整提交列表</summary>\n\n${fullChangelog}\n\n</details>`;
  return `${aiContent}\n\n---\n\n> [!NOTE]\n> 本次发布包含 ${commitCount} 条提交，${stats}。详细变更记录请查看下方完整提交列表。\n\n${foldedChangelog}\n`;
}

function buildFallbackContent(version, prevVersion, commits, commitCount, stats) {
  const fallbackChangelog = buildFullChangelog(commits);
  return `## 更新内容\n\n> [!NOTE]\n> AI 生成失败，以下为自动汇总的提交列表（${prevVersion} → v${version}）。\n\n${fallbackChangelog}\n\n## 📊 变更统计\n\n- 提交数：${commitCount}\n- ${stats}\n`;
}

async function callAI(prompt) {
  const headers = {
    Authorization: `Bearer ${process.env.AI_API_KEY}`,
    'Content-Type': 'application/json',
  };

  if (process.env.CF_ACCESS_CLIENT_ID) {
    headers['CF-Access-Client-Id'] = process.env.CF_ACCESS_CLIENT_ID;
    headers['CF-Access-Client-Secret'] = process.env.CF_ACCESS_CLIENT_SECRET;
  }

  const url = baseUrl + '/chat/completions';
  const maxRetries = 3;
  let lastError;

  for (let attempt = 1; attempt <= maxRetries; attempt++) {
    const t0 = Date.now();
    try {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 300000);
      let response;
      try {
        response = await fetch(url, {
          method: 'POST',
          headers,
          body: JSON.stringify({
            model,
            messages: [{ role: 'user', content: prompt }],
            temperature: 0.6,
          }),
          signal: controller.signal,
        });
      } finally {
        clearTimeout(timeout);
      }

      if (!response.ok) {
        throw new Error(`HTTP ${response.status}: ${(await response.text()).slice(0, 500)}`);
      }

      const data = await response.json();
      let content = data.choices[0].message.content;
      content = content.replace(/^\s*```(?:markdown|md)?\s*\n/i, '').replace(/\n\s*```\s*$/, '');

      const usage = data.usage;
      console.log(
        `AI 已生成 Release Notes（第 ${attempt} 次，${Date.now() - t0}ms）` +
          `，prompt_tokens: ${usage?.prompt_tokens ?? 'n/a'}` +
          `，completion_tokens: ${usage?.completion_tokens ?? 'n/a'}` +
          `，total_tokens: ${usage?.total_tokens ?? 'n/a'}`,
      );
      return content;
    } catch (err) {
      const reason = err.name === 'AbortError' ? '请求超时（>300s）' : err.message;
      console.warn(`第 ${attempt}/${maxRetries} 次尝试失败: ${reason}`);
      lastError = new Error(reason);
      if (attempt < maxRetries) {
        await new Promise((r) => setTimeout(r, 2000 * attempt));
      }
    }
  }

  throw lastError;
}

async function main() {
  const { version, prevVersion: prevFromRange, currentTag } = resolveRange();
  const { prevVersion, commitCount, stats, commits } = collectHistory(version, prevFromRange, currentTag);

  console.log(`版本: v${version}（自 ${prevVersion}）`);
  console.log(`提交数: ${commitCount}`);
  console.log(`变更统计: ${stats}`);
  console.log(`模型: ${model}，baseURL: ${baseUrl}`);

  const prompt = buildPrompt(version, prevVersion, commitCount, commits, stats);
  console.log(`Prompt 大小: ~${Math.round(prompt.length / 2)} tokens（粗略估算）`);

  let finalContent;
  try {
    if (!process.env.AI_API_KEY) {
      throw new Error('未设置 AI_API_KEY');
    }
    const aiContent = await callAI(prompt);
    finalContent = buildFinalContent(aiContent, commits, commitCount, stats);
  } catch (err) {
    console.warn(`AI 生成失败: ${err.message}，回退到简单 changelog`);
    finalContent = buildFallbackContent(version, prevVersion, commits, commitCount, stats);
  }

  fs.writeFileSync(OUTPUT, finalContent, 'utf8');
  console.log(`已写入 ${OUTPUT}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
