<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NDescriptions,
  NDescriptionsItem,
  NEmpty,
  NSkeleton,
  NTag,
} from 'naive-ui'
import { useRuntimeStore } from '@/stores/runtime'
import { formatDateTime } from '@/utils/datetime'
import type { RuntimeRunner } from '@/api/runtime'

const { t, n } = useI18n()
const runtime = useRuntimeStore()
let release: (() => void) | undefined
onMounted(() => {
  release = runtime.subscribe()
})
onUnmounted(() => release?.())

const numeric = (value: number | null | undefined) =>
  value == null ? t('runtime.unavailable') : n(value)
const date = (value: string) => formatDateTime(value, { dateStyle: 'medium', timeStyle: 'medium' })
const runnerMetrics: { key: keyof RuntimeRunner; label: string }[] = [
  { key: 'recovered_total', label: 'recovered' },
  { key: 'recovery_errors_total', label: 'recoveryErrors' },
  { key: 'queue_capacity', label: 'queueCapacity' },
  { key: 'queue_waiting', label: 'queueWaiting' },
  { key: 'enqueue_waiters', label: 'enqueueWaiters' },
  { key: 'worker_capacity', label: 'workerCapacity' },
  { key: 'workers_alive', label: 'workersAlive' },
  { key: 'workers_busy', label: 'workersBusy' },
]
const limiterMetrics = computed(() => {
  const limiter = runtime.snapshot?.limiters
  return [
    { label: 'activeLimiters', value: limiter?.active_limiters },
    { label: 'waiters', value: limiter?.waiters },
    { label: 'waitDuration', value: limiter?.wait_duration_seconds_sum },
    { label: 'waitCount', value: limiter?.wait_duration_seconds_count },
    { label: 'waitCancelled', value: limiter?.wait_cancelled_total },
  ]
})
const runnerTone = (state: RuntimeRunner['state']) =>
  state === 'degraded' ? 'warning' : state === 'running' ? 'success' : 'default'
</script>

<template>
  <main class="mx-auto w-full max-w-7xl space-y-6 p-4 sm:p-6" data-testid="runtime-page">
    <header class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold text-lf-text-strong">{{ t('runtime.title') }}</h1>
        <p class="mt-1 text-sm text-lf-text-muted">{{ t('runtime.description') }}</p>
        <p class="mt-2 text-xs text-lf-text-subtle">{{ t('runtime.polling') }}</p>
      </div>
      <NButton
        :loading="runtime.loading"
        :disabled="!runtime.authorized || runtime.forbidden"
        @click="runtime.refresh"
      >
        {{ t('runtime.refresh') }}
      </NButton>
    </header>

    <NAlert v-if="!runtime.authorized || runtime.forbidden" type="error" role="alert">{{
      t('runtime.forbidden')
    }}</NAlert>
    <NAlert v-else-if="runtime.error" :type="runtime.stale ? 'warning' : 'error'" role="alert">
      <p v-if="runtime.stale">{{ t('runtime.stale') }}</p>
      <p>{{ runtime.error }}</p>
      <NButton
        v-if="!runtime.snapshot"
        class="mt-2"
        size="small"
        :loading="runtime.loading"
        @click="runtime.refresh"
        >{{ t('runtime.retry') }}</NButton
      >
    </NAlert>

    <div v-if="runtime.loading && !runtime.snapshot" aria-busy="true" class="space-y-4">
      <NSkeleton height="140px" :sharp="false" />
      <NSkeleton height="260px" :sharp="false" />
    </div>
    <template v-else-if="runtime.snapshot && runtime.authorized && !runtime.forbidden">
      <NCard :title="t('runtime.instance')" size="small">
        <NDescriptions :column="1" label-placement="left">
          <NDescriptionsItem :label="t('runtime.instanceId')"
            ><span class="break-all font-mono" data-testid="runtime-instance">{{
              runtime.snapshot.instance_id
            }}</span></NDescriptionsItem
          >
          <NDescriptionsItem :label="t('runtime.startedAt')">{{
            date(runtime.snapshot.started_at)
          }}</NDescriptionsItem>
          <NDescriptionsItem :label="t('runtime.snapshotTime')"
            ><time :datetime="runtime.snapshot.as_of">{{
              date(runtime.snapshot.as_of)
            }}</time></NDescriptionsItem
          >
          <NDescriptionsItem :label="t('runtime.uptime')">{{
            t('runtime.seconds', { value: numeric(runtime.snapshot.uptime_seconds) })
          }}</NDescriptionsItem>
        </NDescriptions>
      </NCard>

      <section :aria-label="t('runtime.runners')" class="grid gap-4 lg:grid-cols-2">
        <NCard
          v-for="runner in runtime.snapshot.runners"
          :key="runner.task_type"
          :title="t(`runtime.${runner.task_type}`)"
          size="small"
        >
          <template #header-extra
            ><NTag :type="runnerTone(runner.state)" size="small" :bordered="false">{{
              t(`runtime.state.${runner.state}`)
            }}</NTag></template
          >
          <dl class="grid grid-cols-2 gap-4">
            <div v-for="metric in runnerMetrics" :key="metric.key">
              <dt class="text-xs text-lf-text-muted">{{ t(`runtime.${metric.label}`) }}</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-lf-text-strong">
                {{ numeric(runner[metric.key] as number | null) }}
              </dd>
            </div>
          </dl>
        </NCard>
        <NEmpty v-if="!runtime.snapshot.runners.length" :description="t('runtime.noRunners')" />
      </section>

      <NCard :title="t('runtime.limiters')" size="small">
        <template #header-extra
          ><NTag size="small" :bordered="false">{{
            runtime.snapshot.limiters
              ? t(`runtime.state.${runtime.snapshot.limiters.state}`)
              : t('runtime.unavailable')
          }}</NTag></template
        >
        <dl class="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-5">
          <div v-for="metric in limiterMetrics" :key="metric.label">
            <dt class="text-xs text-lf-text-muted">{{ t(`runtime.${metric.label}`) }}</dt>
            <dd class="mt-1 text-lg font-semibold tabular-nums text-lf-text-strong">
              {{ numeric(metric.value) }}
            </dd>
          </div>
        </dl>
      </NCard>

      <section :aria-label="t('runtime.externalRequests')" class="space-y-4">
        <div>
          <h2 class="text-lg font-semibold text-lf-text-strong">
            {{ t('runtime.externalRequests') }}
          </h2>
          <p class="mt-1 text-xs text-lf-text-muted">{{ t('runtime.requestsNote') }}</p>
        </div>
        <NEmpty
          v-if="!runtime.snapshot.external_requests.length"
          :description="t('runtime.noRequests')"
        />
        <NCard
          v-for="request in runtime.snapshot.external_requests"
          :key="`${request.provider}:${request.operation}`"
          size="small"
        >
          <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
            <h3 class="font-medium">
              {{ request.provider }} · {{ t(`runtime.operations.${request.operation}`) }}
            </h3>
            <div class="flex flex-wrap gap-4 text-sm tabular-nums">
              <span
                >{{ t('runtime.inflight') }}
                <strong>{{ numeric(request.http_attempts_inflight) }}</strong></span
              >
              <span
                >{{ t('runtime.attempts') }}
                <strong>{{ numeric(request.http_attempts_total) }}</strong></span
              >
            </div>
          </div>
          <div class="overflow-x-auto">
            <table class="w-full min-w-[460px] text-left text-sm">
              <caption class="sr-only">
                {{
                  request.provider
                }}
                {{
                  t(`runtime.operations.${request.operation}`)
                }}
              </caption>
              <thead class="text-xs text-lf-text-muted">
                <tr>
                  <th scope="col" class="py-2 font-medium">{{ t('runtime.outcome') }}</th>
                  <th scope="col" class="px-3 py-2 text-right font-medium">
                    {{ t('runtime.finished') }}
                  </th>
                  <th scope="col" class="px-3 py-2 text-right font-medium">
                    {{ t('runtime.duration') }}
                  </th>
                  <th scope="col" class="py-2 text-right font-medium">
                    {{ t('runtime.durationCount') }}
                  </th>
                </tr>
              </thead>
              <tbody class="tabular-nums">
                <tr
                  v-for="outcome in request.outcomes"
                  :key="outcome.outcome"
                  class="border-t border-lf-border-soft"
                >
                  <th scope="row" class="py-3 font-normal">
                    {{ t(`runtime.outcomes.${outcome.outcome}`) }}
                  </th>
                  <td class="px-3 py-3 text-right">
                    {{ numeric(outcome.http_attempts_finished_total) }}
                  </td>
                  <td class="px-3 py-3 text-right">
                    {{ numeric(outcome.http_attempt_duration_seconds_sum) }}
                  </td>
                  <td class="py-3 text-right">
                    {{ numeric(outcome.http_attempt_duration_seconds_count) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </NCard>
      </section>
    </template>
    <NEmpty v-else-if="!runtime.error && runtime.authorized" :description="t('runtime.empty')" />
  </main>
</template>
