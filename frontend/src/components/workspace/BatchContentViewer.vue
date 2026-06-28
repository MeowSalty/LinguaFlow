<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NIcon, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const props = defineProps<{
  content: string
  label: string
}>()

const formatted = computed(() => {
  if (!props.content) return null
  return tryParseJson(props.content)
})

const showRaw = ref(false)
const copySuccess = ref(false)

const displayContent = computed(() => {
  if (showRaw.value) return props.content
  return formatted.value?.formatted ?? props.content
})

const handleCopy = async (): Promise<void> => {
  try {
    await navigator.clipboard.writeText(props.content)
    copySuccess.value = true
    setTimeout(() => {
      copySuccess.value = false
    }, 1500)
  } catch {
    // clipboard API may be unavailable
  }
}

const toggleRaw = (): void => {
  showRaw.value = !showRaw.value
}

function tryParseJson(input: string): { formatted: string; valid: boolean } {
  try {
    return { formatted: JSON.stringify(JSON.parse(input), null, 2), valid: true }
  } catch {
    // try stripping markdown fences
    const stripped = input.replace(/^```(?:json)?\s*\n?/i, '').replace(/\n?```\s*$/i, '')
    try {
      return { formatted: JSON.stringify(JSON.parse(stripped), null, 2), valid: true }
    } catch {
      // try removing trailing commas and comments
      const cleaned = stripped.replace(/\/\/.*$/gm, '').replace(/,(\s*[}\]])/g, '$1')
      try {
        return { formatted: JSON.stringify(JSON.parse(cleaned), null, 2), valid: true }
      } catch {
        return { formatted: input, valid: false }
      }
    }
  }
}
</script>

<template>
  <div class="space-y-1.5">
    <div class="flex items-center justify-between">
      <span class="text-xs font-medium text-lf-text-strong">{{ label }}</span>
      <div class="flex items-center gap-1">
        <NTag v-if="formatted && !formatted.valid" size="tiny" type="warning">
          {{ t('workspace.job.events.batch.malformedJson') }}
        </NTag>
        <NButton v-if="formatted && formatted.valid" quaternary size="tiny" @click="toggleRaw">
          {{
            showRaw
              ? t('workspace.job.events.batch.showFormatted')
              : t('workspace.job.events.batch.showRaw')
          }}
        </NButton>
        <NButton quaternary size="tiny" @click="handleCopy">
          <template #icon>
            <NIcon size="14">
              <IconCarbonCopy v-if="!copySuccess" />
              <IconCarbonCheckmark v-else />
            </NIcon>
          </template>
          {{
            copySuccess
              ? t('workspace.job.events.batch.copySuccess')
              : t('workspace.job.events.batch.copy')
          }}
        </NButton>
      </div>
    </div>
    <div v-if="content" class="max-h-60 overflow-auto rounded-lg bg-lf-code-bg p-3">
      <pre
        class="text-xs text-lf-text whitespace-pre-wrap break-all"
      ><code>{{ displayContent }}</code></pre>
    </div>
    <div v-else class="py-2 text-center text-xs text-lf-text-muted">
      {{ t('workspace.job.events.batch.noContent') }}
    </div>
  </div>
</template>
