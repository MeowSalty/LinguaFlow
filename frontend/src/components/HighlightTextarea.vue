<script setup lang="ts">
/** 高亮规则定义 */
export interface HighlightPattern {
  /** 匹配正则 */
  regex: RegExp
  /** 高亮 mark 元素的 CSS class（与 style 二选一，className 优先） */
  className?: string
  /** 高亮 mark 元素的内联 style */
  style?: string
}

const modelValue = defineModel<string>('value', { default: '' })

const { placeholder = '', highlightPatterns } = defineProps<{
  placeholder?: string
  rows?: number
  /** 自定义高亮规则列表；不传时使用默认的 Go template {{.xxx}} 高亮 */
  highlightPatterns?: HighlightPattern[]
}>()

const textareaRef = ref<HTMLTextAreaElement | null>(null)
const backdropInnerRef = ref<HTMLDivElement | null>(null)

const backdropWidth = ref(0)
const backdropHeight = ref(0)

let resizeObserver: ResizeObserver | null = null

/** 同步背景层尺寸为 textarea 的 clientWidth/clientHeight */
function syncDimensions() {
  if (!textareaRef.value) return
  backdropWidth.value = textareaRef.value.clientWidth
  backdropHeight.value = textareaRef.value.clientHeight
}

/** 默认的 Go template 高亮规则 */
const defaultPattern: HighlightPattern = {
  regex: /(\{\{\.)([\w.]+)(\}\})/g,
  className:
    'rounded px-[1px] bg-brand-100 text-transparent dark:bg-brand-900/40 dark:text-transparent',
}

/** 根据规则列表生成高亮 HTML */
const highlightedHtml = computed(() => {
  const text = modelValue.value || ''
  if (!text) return ''

  // 转义 HTML 特殊字符
  const escaped = text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')

  // 使用传入的规则列表，或使用默认规则
  const patterns = highlightPatterns?.length ? highlightPatterns : [defaultPattern]

  // 按顺序应用每条规则（后续规则在前一轮替换结果上继续匹配）
  const highlighted = patterns.reduce((result, { regex, className, style }) => {
    const cls = className ? ` class="${className}"` : ''
    const sty = style ? ` style="${style}"` : ''
    // 确保正则带 g 标志
    const re = regex.global ? regex : new RegExp(regex.source, regex.flags + 'g')
    return result.replace(re, `<mark${cls}${sty}>$&</mark>`)
  }, escaped)

  // 将换行符转为 <br>，与背景层保持一致的换行行为
  return highlighted.replace(/\n/g, '<br>')
})

/** 滚动同步：通过 transform 平移内层，避免 scrollHeight 不一致导致的偏移 */
let rafId: number | null = null

function syncScroll() {
  if (rafId) return
  rafId = requestAnimationFrame(() => {
    if (textareaRef.value && backdropInnerRef.value) {
      const left = textareaRef.value.scrollLeft
      const top = textareaRef.value.scrollTop
      backdropInnerRef.value.style.transform = `translate(-${left}px, -${top}px)`
    }
    rafId = null
  })
}

/** 将 {{.varName}} 插入到光标位置 */
const insertAtCursor = (placeholder: string): void => {
  const el = textareaRef.value
  if (!el) {
    modelValue.value += placeholder
    return
  }

  const start = el.selectionStart
  const end = el.selectionEnd
  const before = modelValue.value.slice(0, start)
  const after = modelValue.value.slice(end)
  modelValue.value = before + placeholder + after

  nextTick(() => {
    const cursorPos = start + placeholder.length
    el.setSelectionRange(cursorPos, cursorPos)
    el.focus()
  })
}

// ---------- 生命周期 ----------
onMounted(async () => {
  await nextTick()
  syncDimensions()
  if (textareaRef.value) {
    resizeObserver = new ResizeObserver(() => {
      syncDimensions()
      syncScroll()
    })
    resizeObserver.observe(textareaRef.value)
  }
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  if (rafId) cancelAnimationFrame(rafId)
})

defineExpose({ insertAtCursor })
</script>

<template>
  <div
    class="prompt-editor relative overflow-hidden rounded-lg border border-lf-border bg-lf-surface"
  >
    <!-- 背景高亮容器：宽高与 textarea 可视区域一致，overflow: hidden 防止独立滚动 -->
    <div
      ref="backdropRef"
      class="prompt-editor-backdrop pointer-events-none absolute top-0 left-0 text-transparent bg-lf-surface"
      :style="{ width: backdropWidth + 'px', height: backdropHeight + 'px' }"
      aria-hidden="true"
    >
      <!-- 内层平移层，通过 transform 同步滚动 -->
      <div ref="backdropInnerRef" class="prompt-editor-backdrop-inner">
        <span v-html="highlightedHtml"></span>
      </div>
    </div>
    <!-- 输入层：透明文本 -->
    <textarea
      ref="textareaRef"
      :value="modelValue"
      :placeholder="placeholder"
      :rows="rows ?? 6"
      class="prompt-editor-textarea relative z-2 w-full resize-y bg-transparent p-3 text-sm leading-6 text-lf-text outline-none"
      @input="modelValue = ($event.target as HTMLTextAreaElement).value"
      @scroll="syncScroll"
    />
  </div>
</template>

<style scoped>
.prompt-editor:focus-within {
  border-color: var(--n-border-hover);
  box-shadow: 0 0 0 2px rgba(24, 160, 88, 0.15);
}

.prompt-editor-backdrop,
.prompt-editor-textarea {
  font-family:
    'Cascadia Code', 'Fira Code', 'JetBrains Mono', Menlo, Monaco, Consolas, 'Liberation Mono',
    'Courier New', 'Source Han Mono SC', 'Noto Sans Mono CJK SC', 'WenQuanYi Micro Hei Mono',
    monospace;
  font-variant-ligatures: none;
  tab-size: 4;
  min-height: 6rem;
}

/* 背景高亮层：与 textarea 完全一致的排版样式 */
.prompt-editor-backdrop {
  box-sizing: border-box;
  border-radius: inherit;
  padding: 12px; /* p-3 */
  font-size: 0.875rem; /* text-sm */
  line-height: 1.5rem; /* leading-6 */
  white-space: pre-wrap;
  word-wrap: break-word;
  overflow: hidden;
  z-index: 1;
}

/* 内层平移层：保持与 textarea 内容区完全一样的排版 */
.prompt-editor-backdrop-inner {
  width: 100%;
  height: fit-content;
  white-space: pre-wrap;
  word-wrap: break-word;
}

/* 重置 textarea 的浏览器 UA 默认样式，确保与背景层像素级对齐 */
.prompt-editor-textarea {
  margin: 0;
  border: none;
  resize: vertical;
}

.prompt-editor-textarea::placeholder {
  color: var(--lf-text-subtle, #9ca3af);
}
</style>
