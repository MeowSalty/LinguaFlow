#!/usr/bin/env node
/**
 * 通过 Server酱³（https://sc3.ft07.com/）推送消息到手机。
 *
 * CI 用法（由 release.yml / release-docker.yml 调用），参数经环境变量传入：
 *   SC3_SEND_URL  必填  Server酱³后台复制的完整发送地址，
 *                       形如 https://<uid>.push.ft07.com/send/<sendkey>.send；
 *                       未配置时静默跳过（推送是可选增强，不配置不影响 CI）
 *   SC3_TITLE     必填  推送标题
 *   SC3_DESP      选填  正文，支持 Markdown
 *   SC3_SHORT     选填  消息卡片的简短描述
 *   SC3_TAGS      选填  标签，多个用竖线分隔
 *
 * 推送失败只告警不报错：通知是尽力而为的投递，不能让它挂掉发布流程本身。
 */

async function main() {
  const sendUrl = process.env.SC3_SEND_URL?.trim();
  if (!sendUrl) {
    console.log('未配置 SC3_SEND_URL，跳过推送');
    return;
  }

  const payload = { title: process.env.SC3_TITLE };
  if (!payload.title) throw new Error('未设置 SC3_TITLE');
  if (process.env.SC3_DESP) payload.desp = process.env.SC3_DESP;
  if (process.env.SC3_SHORT) payload.short = process.env.SC3_SHORT;
  if (process.env.SC3_TAGS) payload.tags = process.env.SC3_TAGS;

  const response = await fetch(sendUrl, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const body = await response.text();
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}: ${body.slice(0, 300)}`);
  }
  console.log(`推送成功: ${body.slice(0, 300)}`);
}

main().catch((err) => {
  console.warn(`Server酱推送失败（不影响 CI 结果）: ${err.message}`);
});
