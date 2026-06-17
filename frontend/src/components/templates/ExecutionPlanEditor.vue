<script setup lang="ts">
import {
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NGrid,
  NGi,
  NInput,
  NInputNumber,
  NSelect,
  NSwitch,
} from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type RetryConfig = NonNullable<ExecutionRoundConfig['retry']>

/** 内部轮次模型：确保 retry 始终存在（API schema 中 retry 是可选的） */
type RoundModel = Omit<ExecutionRoundConfig, 'retry'> & { retry: RetryConfig }

// ─── 默认值 ──────────────────────────────────────────────────

const DEFAULT_RETRY: RetryConfig = { max_attempts: 3, backoff_ms: 2000, jitter: true }

const DEFAULT_ROUND: RoundModel = {
  backend_id: 0,
  prompt_template_id: 0,
  profile_id: 0,
  batch_size: 10,
  concurrency: 3,
  fallback_shrink: 0,
  rate_limit_per_sec: 0,
  retry: { ...DEFAULT_RETRY },
}

// ─── 工具函数 ────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

/** 确保轮次所有字段都有默认值（处理 API 返回的可选字段） */
function mergeRound(source?: Partial<ExecutionRoundConfig>): RoundModel {
  if (!source) return deepClone(DEFAULT_ROUND)
  return {
    name: source.name,
    backend_id: source.backend_id ?? DEFAULT_ROUND.backend_id,
    prompt_template_id: source.prompt_template_id ?? DEFAULT_ROUND.prompt_template_id,
    profile_id: source.profile_id ?? DEFAULT_ROUND.profile_id,
    batch_size: source.batch_size ?? DEFAULT_ROUND.batch_size,
    concurrency: source.concurrency ?? DEFAULT_ROUND.concurrency,
    fallback_shrink: source.fallback_shrink ?? DEFAULT_ROUND.fallback_shrink,
    rate_limit_per_sec: source.rate_limit_per_sec ?? DEFAULT_ROUND.rate_limit_per_sec,
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    rounds: ExecutionRoundConfig[]
    backends: SelectOption[]
    promptTemplates: SelectOption[]
    translationProfiles: SelectOption[]
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:rounds': [value: ExecutionRoundConfig[]]
}>()

// ─── 内部状态 ────────────────────────────────────────────────

const { t } = useI18n()

const roundsModel = ref<RoundModel[]>(props.rounds.map((r) => mergeRound(r)))

// 上次 emit 的 JSON（用于去重）
let lastRoundsJson = JSON.stringify(props.rounds ?? [])

// 监听外部 rounds 变化
watch(
  () => props.rounds,
  (newVal) => {
    const json = JSON.stringify(newVal ?? [])
    if (json === lastRoundsJson) return
    roundsModel.value = (newVal ?? []).map((r) => mergeRound(r))
  },
  { deep: true },
)

// 监听内部 rounds 变化并 emit
watch(
  roundsModel,
  (newVal) => {
    const json = JSON.stringify(newVal)
    if (json === lastRoundsJson) return
    lastRoundsJson = json
    emit('update:rounds', deepClone(newVal))
  },
  { deep: true },
)

// ─── 操作方法 ────────────────────────────────────────────────

const addRound = (): void => {
  roundsModel.value.push(deepClone(DEFAULT_ROUND))
  emitUpdate()
}

const removeRound = (index: number): void => {
  if (roundsModel.value.length <= 1) return
  roundsModel.value.splice(index, 1)
  emitUpdate()
}

const moveRound = (index: number, direction: -1 | 1): void => {
  const newIndex = index + direction
  if (newIndex < 0 || newIndex >= roundsModel.value.length) return
  const temp = roundsModel.value[index]
  roundsModel.value[index] = roundsModel.value[newIndex]!
  roundsModel.value[newIndex] = temp!
  emitUpdate()
}

const emitUpdate = (): void => {
  emit('update:rounds', deepClone(roundsModel.value))
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 轮次列表 -->
    <NCard
      v-for="(round, index) in roundsModel"
      :key="index"
      size="small"
      :bordered="true"
      class="relative"
    >
      <template #header>
        <div class="flex items-center gap-2">
          <span
            class="inline-flex h-6 w-6 items-center justify-center rounded-full bg-lf-brand-soft text-xs font-bold text-brand-600"
          >
            {{ index + 1 }}
          </span>
          <span class="text-sm font-semibold">
            {{ round.name || `round-${index + 1}` }}
          </span>
        </div>
      </template>

      <template #header-extra>
        <div class="flex items-center gap-1">
          <NButton
            text
            size="small"
            :disabled="disabled || index === 0"
            @click="moveRound(index, -1)"
          >
            ▲
          </NButton>
          <NButton
            text
            size="small"
            :disabled="disabled || index === roundsModel.length - 1"
            @click="moveRound(index, 1)"
          >
            ▼
          </NButton>
          <NButton
            text
            type="error"
            size="small"
            :disabled="disabled || roundsModel.length <= 1"
            @click="removeRound(index)"
          >
            ✕
          </NButton>
        </div>
      </template>

      <!-- 轮次名称 -->
      <div class="mb-1 text-xs text-lf-text-subtle">
        {{ t('executionPlanEditor.round.name') }}
      </div>
      <NInput
        v-model:value="round.name"
        :placeholder="t('executionPlanEditor.round.namePlaceholder', { n: index + 1 })"
        size="small"
        :disabled="disabled"
      />

      <!-- 资源选择 -->
      <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-3">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.backend') }}
            <span class="text-red-400">*</span>
          </div>
          <NSelect
            v-model:value="round.backend_id"
            :options="backends"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.backendPlaceholder')"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.promptTemplate') }}
            <span class="text-red-400">*</span>
          </div>
          <NSelect
            v-model:value="round.prompt_template_id"
            :options="promptTemplates"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.promptTemplatePlaceholder')"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.translationProfile') }}
            <span class="text-red-400">*</span>
          </div>
          <NSelect
            v-model:value="round.profile_id"
            :options="translationProfiles"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.profilePlaceholder')"
          />
        </div>
      </div>

      <!-- 执行参数 -->
      <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-3">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.batchSize') }}
            <span class="text-red-400">*</span>
          </div>
          <NInputNumber
            v-model:value="round.batch_size"
            :min="1"
            :max="10000"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.concurrency') }}
            <span class="text-red-400">*</span>
          </div>
          <NInputNumber
            v-model:value="round.concurrency"
            :min="1"
            :max="100"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.fallbackShrink') }}
          </div>
          <NInputNumber
            v-model:value="round.fallback_shrink"
            :min="0"
            :max="1"
            :step="0.1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
      </div>

      <!-- 高级配置（可折叠） -->
      <NCollapse class="mt-3">
        <NCollapseItem :title="t('executionPlanEditor.round.advancedConfig')">
          <NGrid :cols="3" :x-gap="12" :y-gap="10">
            <NGi>
              <div class="mb-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.rateLimitPerSec') }}
              </div>
              <NInputNumber
                v-model:value="round.rate_limit_per_sec"
                :min="0"
                size="small"
                :disabled="disabled"
                class="w-full"
              />
            </NGi>
            <NGi>
              <div class="mb-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.retryMaxAttempts') }}
              </div>
              <NInputNumber
                v-model:value="round.retry.max_attempts"
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
                v-model:value="round.retry.backoff_ms"
                :min="0"
                :max="60000"
                :step="100"
                size="small"
                :disabled="disabled"
                class="w-full"
              />
            </NGi>
          </NGrid>
          <div class="mt-2 flex items-center gap-2">
            <NSwitch v-model:value="round.retry.jitter" size="small" :disabled="disabled" />
            <span class="text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.retryJitter') }}
            </span>
          </div>
        </NCollapseItem>
      </NCollapse>
    </NCard>

    <!-- 添加轮次按钮 -->
    <NButton dashed block :disabled="disabled" @click="addRound">
      {{ t('executionPlanEditor.actions.addRound') }}
    </NButton>
  </div>
</template>
