<script setup lang="ts">
const modelValue = defineModel<string>('value', { default: '' })

const {
  placeholder = '',
  rows = 6,
  disabled = false,
} = defineProps<{
  placeholder?: string
  rows?: number
  disabled?: boolean
}>()

const textareaRef = ref<HTMLTextAreaElement | null>(null)
const backdropInnerRef = ref<HTMLDivElement | null>(null)

let resizeObserver: ResizeObserver | null = null

/** 同步背景层高度为 textarea 的实际高度 */
function syncHeight() {
  if (!textareaRef.value) return
  const height = textareaRef.value.offsetHeight
  backdropInnerRef.value?.parentElement?.style.setProperty('height', height + 'px')
}

// --- Go Template 分词高亮 ---

/** HTML 特殊字符转义 */
function escapeHtml(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/** Go template 关键字集合 */
const GO_TEMPLATE_KEYWORDS = new Set([
  'if',
  'else',
  'end',
  'range',
  'with',
  'template',
  'define',
  'block',
])

/** Token 类型 */
type TokenType =
  | 'text'
  | 'open-delim'
  | 'close-delim'
  | 'keyword'
  | 'field'
  | 'function'
  | 'string'
  | 'pipe'
  | 'other'

/** 分词结果 */
interface Token {
  type: TokenType
  value: string
}

/** 模板动作的正则：匹配 {{ 或 {{- 开头，}} 或 -}} 结尾 */
const ACTION_RE = /\{\{-?[\s\S]*?-?\}\}/g

/** 模板动作内部的分词正则 */
const INNER_RE = /\s+|"[^"]*"|(\.)[a-zA-Z_]\w*(?:\.\w+)*|[a-zA-Z_]\w*|\||./g

/**
 * 将已转义的文本分词为 Go template token 序列。
 * 第一层：切分普通文本与模板动作；第二层：模板动作内部细分。
 */
function tokenizeTemplate(escapedText: string): Token[] {
  const tokens: Token[] = []
  let lastIndex = 0

  for (const match of escapedText.matchAll(ACTION_RE)) {
    // 匹配前的普通文本
    if (match.index > lastIndex) {
      tokens.push({ type: 'text', value: escapedText.slice(lastIndex, match.index) })
    }

    const action = match[0]
    // 提取左定界符
    const leftDelim = action.startsWith('{{-') ? '{{-' : '{{'
    // 提取右定界符
    const rightDelim = action.endsWith('-}}') ? '-}}' : '}}'
    // 中间内容
    const content = action.slice(leftDelim.length, -rightDelim.length)

    tokens.push({ type: 'open-delim', value: leftDelim })

    // 第二层：内容细分
    for (const m of content.matchAll(INNER_RE)) {
      const val = m[0]
      if (val[0] === '"') {
        tokens.push({ type: 'string', value: val })
      } else if (m[1] === '.') {
        tokens.push({ type: 'field', value: val })
      } else if (val.length > 0 && /[a-zA-Z_]/.test(val[0]!)) {
        tokens.push({
          type: GO_TEMPLATE_KEYWORDS.has(val) ? 'keyword' : 'function',
          value: val,
        })
      } else if (val === '|') {
        tokens.push({ type: 'pipe', value: val })
      } else {
        tokens.push({ type: 'other', value: val })
      }
    }

    tokens.push({ type: 'close-delim', value: rightDelim })
    lastIndex = match.index + action.length
  }

  // 末尾剩余文本
  if (lastIndex < escapedText.length) {
    tokens.push({ type: 'text', value: escapedText.slice(lastIndex) })
  }

  return tokens
}

/** 将 token 序列渲染为带语义 class 的高亮 HTML */
function renderTokens(tokens: Token[]): string {
  let result = ''
  let inAction = false

  for (const token of tokens) {
    const val = token.value

    if (token.type === 'text') {
      result += val
      continue
    }

    if (token.type === 'open-delim') {
      result += `<mark class="tmpl-action"><mark class="tmpl-delim">${val}</mark>`
      inAction = true
    } else if (token.type === 'close-delim') {
      result += `<mark class="tmpl-delim">${val}</mark></mark>`
      inAction = false
    } else if (inAction) {
      switch (token.type) {
        case 'keyword':
          result += `<mark class="tmpl-keyword">${val}</mark>`
          break
        case 'field':
          result += `<mark class="tmpl-field">${val}</mark>`
          break
        case 'function':
          result += `<mark class="tmpl-func">${val}</mark>`
          break
        case 'string':
          result += `<mark class="tmpl-string">${val}</mark>`
          break
        case 'pipe':
          result += `<mark class="tmpl-pipe">${val}</mark>`
          break
        default:
          result += val
      }
    }
  }

  return result
}

/** Go template 高亮入口：转义文本 → 分词 → 渲染 HTML */
function highlightGoTemplate(text: string): string {
  const escaped = escapeHtml(text)
  const tokens = tokenizeTemplate(escaped)
  return renderTokens(tokens)
}

// --- 高亮 HTML 计算 ---

/** 使用 Go template 分词高亮生成 HTML */
const highlightedHtml = computed(() => {
  const text = modelValue.value || ''
  if (!text) return ''

  const highlighted = highlightGoTemplate(text)

  // 将换行符转为 <br>，与背景层保持一致的换行行为
  return highlighted.replace(/\r\n/g, '\n').replace(/\n/g, '<br>')
})

// --- 滚动同步 ---

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

/** 将模板变量插入到光标位置 */
function insertAtCursor(placeholder: string): void {
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

// 内容变化时同步滚动位置
watch(modelValue, () => {
  nextTick(() => {
    syncScroll()
  })
})

// 生命周期
onMounted(() => {
  nextTick(() => {
    syncHeight()
    if (textareaRef.value) {
      resizeObserver = new ResizeObserver(() => {
        syncHeight()
        syncScroll()
      })
      resizeObserver.observe(textareaRef.value)
    }
  })
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  if (rafId) cancelAnimationFrame(rafId)
})

defineExpose({ insertAtCursor })
</script>

<template>
  <div
    class="prompt-editor relative overflow-hidden rounded-lg border border-lf-border bg-lf-surface"
  >
    <!-- 背景高亮容器：通过 CSS left/right 与 textarea 完全对齐，无需 JS 计算宽度 -->
    <div
      class="prompt-editor-backdrop pointer-events-none absolute top-0 left-0 right-0 text-lf-text bg-lf-surface"
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
      :rows="rows"
      :disabled="disabled"
      class="prompt-editor-textarea relative z-2 w-full bg-transparent p-3 text-sm leading-6 text-transparent caret-lf-text outline-none disabled:cursor-not-allowed disabled:opacity-50"
      @input="modelValue = ($event.target as HTMLTextAreaElement).value"
      @scroll="syncScroll"
    />
  </div>
</template>

<!--
  Go template 高亮样式：非 scoped，通过 .prompt-editor 选择器限定作用域。
  v-html 动态插入的 <mark> 元素不带 Vue scoped 标记，scoped 样式无法匹配。
-->
<style>
/* 重置浏览器默认的 <mark> 黄色背景，只允许 .tmpl-action 设置背景色 */
.prompt-editor mark {
  background-color: transparent;
  padding: 0;
}

.prompt-editor .tmpl-action {
  background-color: var(--lf-tmpl-bg);
  border-radius: 2px;
  padding: 1px 0;
}

.prompt-editor .tmpl-delim {
  color: var(--lf-tmpl-delim);
}

.prompt-editor .tmpl-keyword {
  color: var(--lf-tmpl-keyword);
  font-weight: 500;
}

.prompt-editor .tmpl-field {
  color: var(--lf-tmpl-field);
}

.prompt-editor .tmpl-func {
  color: var(--lf-tmpl-func);
}

.prompt-editor .tmpl-string {
  color: var(--lf-tmpl-string);
}

.prompt-editor .tmpl-pipe {
  color: var(--lf-tmpl-pipe);
  font-weight: 600;
}
</style>

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
