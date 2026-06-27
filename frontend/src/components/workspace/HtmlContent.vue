<script setup lang="ts">
interface HtmlContentProps {
  content: string
  /** 是否为纯文本模式（不渲染 HTML） */
  plainText?: boolean
  /** 最大显示行数（超出折叠） */
  maxLines?: number
}

const props = withDefaults(defineProps<HtmlContentProps>(), {
  plainText: false,
  maxLines: undefined,
})

// 简单的 HTML 白名单过滤（不依赖 dompurify）
// 保留安全的内联标签，移除 script/style/iframe 等危险标签
const sanitizedHtml = computed(() => {
  if (props.plainText) return props.content

  // 移除危险标签及其内容
  let html = props.content.replace(
    /<(script|style|iframe|object|embed|form|input|textarea|button|select|link|meta|base)[\s\S]*?<\/\1>|<(script|style|iframe|object|embed|form|input|textarea|button|select|link|meta|base)[\s\S]*?\/?>/gi,
    '',
  )

  // 移除所有 on* 事件属性
  html = html.replace(/\s+on\w+\s*=\s*("[^"]*"|'[^']*'|[^\s>]*)/gi, '')

  // 移除 javascript: 协议
  html = html.replace(/href\s*=\s*["']?\s*javascript:/gi, 'href="#"')

  return html
})

// 检查内容是否包含 HTML 标签
const hasHtml = computed(() => /<[a-z][\s\S]*>/i.test(props.content))

const clampedStyle = computed(() => {
  if (!props.maxLines) return {}
  return {
    WebkitLineClamp: String(props.maxLines),
    display: '-webkit-box',
    WebkitBoxOrient: 'vertical' as const,
    overflow: 'hidden',
  }
})
</script>

<template>
  <div v-if="plainText || !hasHtml" class="html-content">{{ content }}</div>
  <div v-else class="html-content" :style="clampedStyle" v-html="sanitizedHtml" />
</template>

<style scoped>
.html-content :deep(strong),
.html-content :deep(b) {
  font-weight: 600;
  color: var(--lf-text-strong, inherit);
}

.html-content :deep(em),
.html-content :deep(i) {
  font-style: italic;
}

.html-content :deep(ruby) {
  ruby-align: center;
}

.html-content :deep(rt) {
  font-size: 0.7em;
  color: var(--lf-text-muted, #999);
}

.html-content :deep(a) {
  color: var(--color-brand-500, #3b82f6);
  text-decoration: underline;
  text-underline-offset: 2px;
}
</style>
