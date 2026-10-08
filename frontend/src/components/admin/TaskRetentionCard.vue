<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import {
  NAlert,
  NButton,
  NCheckbox,
  NForm,
  NFormItem,
  NInputNumber,
  NModal,
  NSkeleton,
  NSwitch,
  NTag,
  useMessage,
} from 'naive-ui'
import type { FormRules } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useAdminStore } from '@/stores/admin'
import { retentionCountLabel } from '@/api/task-history'
import { retentionDate, retentionReason } from '@/composables/taskRetentionPresentation'
import { validRetentionDays, type TaskRetentionController } from '@/composables/useTaskRetention'
import TaskRetentionPreview from './TaskRetentionPreview.vue'

const props = defineProps<{ controller: TaskRetentionController }>()
const admin = useAdminStore()
const { t } = useI18n()
const message = useMessage()
const {
  draft,
  policy,
  preview,
  previewLoading,
  previewError,
  status,
  statusLoading,
  statusError,
  conflict,
  conflictReadFailed,
  preparing,
  submitting,
  confirmation,
  partialAccepted,
  notice,
  hasChanges,
  valid,
  busy,
  canSave,
} = props.controller
const saveButton = ref<{ $el: HTMLButtonElement } | null>(null)
const cancelButton = ref<{ $el: HTMLButtonElement } | null>(null)
const previewBusy = computed(() => busy.value || previewLoading.value || conflict.value)
const rules = computed<FormRules>(() => ({
  retention_days: {
    trigger: ['blur', 'change'],
    validator: (_rule, value) =>
      validRetentionDays(value) || new Error(t('taskRetention.daysValidation')),
  },
}))
const hasSkipped = computed(
  () =>
    !!status.value?.last_scan &&
    Object.values(status.value.last_scan.skipped).some((value) => value > 0),
)
const readPreview = () => {
  void props.controller.readPreview()
}
const save = async () => {
  if (await props.controller.save()) message.success(t('taskRetention.saved'))
}
const confirmSave = async () => {
  if (await props.controller.confirmSave()) message.success(t('taskRetention.saved'))
}
const focusCancel = () => {
  void nextTick(() => cancelButton.value?.$el?.focus())
}
const restoreFocus = () => {
  void nextTick(() => saveButton.value?.$el?.focus())
}
</script>

<template>
  <section class="lf-panel space-y-5 p-5 sm:p-6" aria-labelledby="retention-title">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h2 id="retention-title" class="text-base font-semibold text-lf-text-strong">
        {{ t('taskRetention.title') }}
      </h2>
      <NTag v-if="hasChanges" size="small" type="warning">{{
        t('configurationSettings.unsaved')
      }}</NTag>
    </div>
    <NSkeleton v-if="admin.settingsLoading && !draft" text :repeat="4" />
    <template v-else-if="draft && policy">
      <div class="space-y-1 text-sm text-lf-text-muted" aria-live="polite">
        <p>
          {{
            t(policy.enabled ? 'taskRetention.savedEnabled' : 'taskRetention.savedDisabled', {
              days: policy.retention_days,
            })
          }}
        </p>
        <p class="text-xs">{{ t('taskRetention.revision', { revision: policy.revision }) }}</p>
      </div>
      <NForm :model="draft" :rules="rules" :disabled="busy" label-placement="top">
        <div class="mb-4 flex items-center justify-between gap-4">
          <label id="retention-enabled-label" class="text-sm font-medium">{{
            t('taskRetention.enabled')
          }}</label>
          <NSwitch
            :value="draft.enabled"
            :disabled="busy"
            aria-labelledby="retention-enabled-label"
            aria-describedby="retention-effect"
            @update:value="controller.updateDraft({ enabled: $event })"
          />
        </div>
        <NFormItem
          :label="t('taskRetention.days')"
          path="retention_days"
          :validation-status="valid ? undefined : 'error'"
          :feedback="valid ? undefined : t('taskRetention.daysValidation')"
        >
          <NInputNumber
            :value="draft.retention_days"
            :disabled="busy"
            :min="1"
            :max="3650"
            :show-button="false"
            class="w-full sm:max-w-56"
            :aria-label="t('taskRetention.days')"
            :input-props="{
              role: 'spinbutton',
              'aria-label': t('taskRetention.days'),
              'aria-valuemin': 1,
              'aria-valuemax': 3650,
              'aria-valuenow': draft.retention_days ?? undefined,
            }"
            @update:value="controller.updateDraft({ retention_days: $event })"
          />
        </NFormItem>
        <p id="retention-effect" class="text-sm leading-relaxed text-lf-text-muted">
          {{ t('taskRetention.description') }}
        </p>
      </NForm>
      <NAlert v-if="conflict" type="warning" role="alert">
        <p class="font-medium">{{ t('taskRetention.conflict') }}</p>
        <p class="mt-2">
          {{
            t(
              conflictReadFailed
                ? 'taskRetention.conflictReadFailed'
                : 'taskRetention.conflictHint',
            )
          }}
        </p>
        <div class="mt-3 flex flex-wrap gap-2">
          <NButton v-if="conflictReadFailed" :disabled="busy" @click="controller.reloadConflict">{{
            t('taskRetention.reloadLatest')
          }}</NButton>
          <template v-else>
            <NButton :disabled="busy" @click="controller.resetDraft">{{
              t('taskRetention.useServer')
            }}</NButton>
            <NButton :disabled="busy" @click="controller.useLatest">{{
              t('taskRetention.useLatest')
            }}</NButton>
          </template>
        </div>
      </NAlert>
      <NAlert v-if="notice" type="info">{{ notice }}</NAlert>
      <div class="space-y-4 border-t border-lf-border pt-5">
        <NButton
          secondary
          :loading="previewLoading"
          :disabled="previewBusy || !valid"
          @click="readPreview"
          >{{ t('taskRetention.preview') }}</NButton
        >
        <NAlert v-if="previewError" type="error" role="alert"
          >{{ t('taskRetention.previewFailed') }} {{ previewError }}</NAlert
        >
        <TaskRetentionPreview v-if="preview" :preview="preview" />
      </div>
      <section
        class="space-y-3 border-t border-lf-border pt-5"
        aria-labelledby="retention-status-title"
      >
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h3 id="retention-status-title" class="text-sm font-semibold">
            {{ t('taskRetention.statusTitle') }}
          </h3>
          <NButton
            size="small"
            secondary
            :loading="statusLoading"
            :disabled="statusLoading"
            @click="controller.refreshStatus(true)"
            >{{ t('taskRetention.refreshStatus') }}</NButton
          >
        </div>
        <NAlert v-if="statusError" type="error" role="alert"
          >{{ statusError
          }}<span v-if="status"> · {{ t('taskRetention.statusStale') }}</span></NAlert
        >
        <NSkeleton v-if="statusLoading && !status" text :repeat="2" />
        <template v-if="status">
          <p v-if="status.policy_revision !== policy.revision" class="text-xs text-lf-text-muted">
            {{ t('taskRetention.statusStale') }}
          </p>
          <p class="text-sm font-medium">{{ t(`taskRetention.states.${status.state}`) }}</p>
          <p
            v-for="(code, index) in status.reason_codes"
            :key="index"
            class="text-sm text-lf-text-muted"
          >
            {{ retentionReason(code) }}
          </p>
          <p class="text-sm text-lf-text-muted">
            {{
              t(
                status.backlog === null
                  ? 'taskRetention.backlogUnknown'
                  : status.backlog
                    ? 'taskRetention.backlogYes'
                    : 'taskRetention.backlogNo',
              )
            }}
          </p>
          <div
            v-if="status.last_scan"
            class="space-y-2 rounded-lg border border-lf-border p-3 text-sm"
          >
            <p>
              {{ t('taskRetention.lastScan') }} · {{ retentionDate(status.last_scan.finished_at) }}
            </p>
            <p
              v-if="status.last_scan.policy_revision !== policy.revision"
              class="text-lf-text-muted"
            >
              {{ t('taskRetention.previousPolicy') }}
            </p>
            <dl class="flex flex-wrap gap-x-6 gap-y-2">
              <div>
                <dt class="text-xs text-lf-text-muted">{{ t('taskRetention.candidates') }}</dt>
                <dd class="tabular-nums">{{ retentionCountLabel(status.last_scan.candidates) }}</dd>
              </div>
              <div>
                <dt class="text-xs text-lf-text-muted">{{ t('taskRetention.deleted') }}</dt>
                <dd class="tabular-nums">{{ retentionCountLabel(status.last_scan.deleted) }}</dd>
              </div>
            </dl>
            <p>
              {{
                t(
                  status.last_scan.completed
                    ? 'taskRetention.scanCompleted'
                    : 'taskRetention.scanIncomplete',
                )
              }}
            </p>
            <p v-if="hasSkipped">{{ t('taskRetention.skipped') }}</p>
            <dl v-if="hasSkipped" class="space-y-1">
              <template v-for="(count, code) in status.last_scan.skipped" :key="code"
                ><div v-if="count > 0" class="flex flex-wrap justify-between gap-2">
                  <dt>{{ retentionReason(String(code)) }}</dt>
                  <dd>{{ retentionCountLabel(count) }}</dd>
                </div></template
              >
            </dl>
            <p v-if="status.last_scan.error_code">
              {{ retentionReason(status.last_scan.error_code) }}
            </p>
          </div>
          <p v-else class="text-sm text-lf-text-muted">{{ t('taskRetention.noScan') }}</p>
        </template>
      </section>
      <p class="text-xs leading-relaxed text-lf-text-muted">
        {{ t('taskRetention.boundary') }} {{ t('taskRetention.noRestore') }}
      </p>
      <div class="flex flex-wrap gap-2">
        <NButton
          ref="saveButton"
          type="primary"
          :loading="preparing || submitting"
          :disabled="!canSave"
          @click="save"
          >{{ t('taskRetention.save') }}</NButton
        >
        <NButton :disabled="busy || !hasChanges" @click="controller.resetDraft">{{
          t('taskRetention.discard')
        }}</NButton>
      </div>
    </template>
    <p v-else class="text-sm text-lf-text-muted">{{ t('taskRetention.unconfirmed') }}</p>
    <NModal
      :show="confirmation !== null"
      preset="card"
      :title="t('taskRetention.confirmTitle')"
      style="width: min(640px, calc(100vw - 32px))"
      :auto-focus="false"
      :mask-closable="!submitting"
      :close-on-esc="!submitting"
      :closable="!submitting"
      @update:show="!$event && !submitting && controller.cancelConfirmation()"
      @after-enter="focusCancel"
      @after-leave="restoreFocus"
    >
      <div
        v-if="confirmation"
        class="max-h-[min(65vh,calc(100dvh_-_220px))] space-y-4 overflow-y-auto"
      >
        <p class="font-medium">
          {{
            t(
              confirmation.baseline.enabled ? 'taskRetention.shortening' : 'taskRetention.enabling',
              {
                from: confirmation.baseline.retention_days,
                to: confirmation.patch.retention_days,
                days: confirmation.patch.retention_days,
              },
            )
          }}
        </p>
        <TaskRetentionPreview :preview="confirmation.preview" />
        <p class="text-sm leading-relaxed text-lf-text-muted">{{ t('taskRetention.boundary') }}</p>
        <NCheckbox
          v-if="confirmation.preview.partial"
          v-model:checked="partialAccepted"
          :disabled="submitting"
          >{{ t('taskRetention.acknowledgePartial') }}</NCheckbox
        >
      </div>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-2">
          <NButton
            ref="cancelButton"
            :disabled="submitting"
            @click="controller.cancelConfirmation()"
            >{{ t('common.cancel') }}</NButton
          >
          <NButton
            type="error"
            :loading="submitting"
            :disabled="busy || !confirmation || (confirmation.preview.partial && !partialAccepted)"
            @click="confirmSave"
            >{{
              t(
                confirmation?.baseline.enabled
                  ? 'taskRetention.confirmShorten'
                  : 'taskRetention.confirmEnable',
              )
            }}</NButton
          >
        </div>
      </template>
    </NModal>
  </section>
</template>
