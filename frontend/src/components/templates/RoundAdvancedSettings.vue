<script setup lang="ts">
import { NCollapse, NCollapseItem, NGrid, NGi, NInputNumber, NSwitch } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type RetryConfig = ApiSchemas['RetryConfig']

const props = withDefaults(
  defineProps<{
    round: ExecutionRoundConfig
    disabled?: boolean
  }>(),
  { disabled: false },
)

const { t } = useI18n()

// 五种 LLM 轮次模式的 retry 结构完全一致，仅嵌套路径不同，按 mode 取到对应的 retry 对象引用
const retry = computed<RetryConfig | undefined>(() => {
  const round = props.round
  if (round.mode === 'translate') return round.translate?.retry
  if (round.mode === 'extract') return round.extract?.retry
  if (round.mode === 'adjudicate') return round.adjudicate?.retry
  if (round.mode === 'semantic_qa') return round.semantic_qa?.retry
  if (round.mode === 'revise') return round.revise?.retry
  return undefined
})
</script>

<template>
  <NCollapse class="mt-3">
    <NCollapseItem :title="t('executionPlanEditor.round.advancedConfig')">
      <NGrid cols="1 s:2" responsive="screen" :x-gap="12" :y-gap="10">
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.retryMaxAttempts') }}
          </div>
          <NInputNumber
            v-if="retry"
            v-model:value="retry.max_attempts"
            :min="0"
            :max="10"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.retryBackoffMs') }}
          </div>
          <NInputNumber
            v-if="retry"
            v-model:value="retry.backoff_ms"
            :min="0"
            :max="60000"
            :step="100"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
      </NGrid>
      <div v-if="retry" class="mt-2 flex items-center gap-2">
        <NSwitch v-model:value="retry.jitter" size="small" :disabled="disabled" />
        <span class="text-xs text-lf-text-subtle">
          {{ t('executionPlanEditor.round.retryJitter') }}
        </span>
      </div>
    </NCollapseItem>
  </NCollapse>
</template>
