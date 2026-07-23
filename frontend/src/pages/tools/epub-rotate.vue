<script setup lang="ts">
import { Icon as IconifyIcon } from '@iconify/vue'
import { useMessage, type UploadFileInfo } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useEpubRotate, type EpubRotateMode } from '@/composables/useEpubRotate'

const { t } = useI18n()
const message = useMessage()
const { stage, progress, result, error, busy, convert, reset, downloadResult } = useEpubRotate()

const selectedFile = ref<File | null>(null)
const fileList = ref<UploadFileInfo[]>([])
const mode = ref<EpubRotateMode>('auto')

const modeOptions = computed(() => [
  { label: t('epubRotate.modeAuto'), value: 'auto' as const },
  { label: t('epubRotate.convertToHorizontal'), value: 'horizontal' as const },
  { label: t('epubRotate.convertToVertical'), value: 'vertical' as const },
])

const progressPercent = computed(() => {
  if (stage.value === 'done') return 100
  if (stage.value === 'packing') return 95
  if (stage.value === 'parsing') return 8
  if (progress.value.total <= 0) return 0
  return Math.min(90, Math.round((progress.value.current / progress.value.total) * 90) + 8)
})

const stageLabel = computed(() => {
  switch (stage.value) {
    case 'parsing':
      return t('epubRotate.stageParsing')
    case 'converting':
      return t('epubRotate.stageConverting', {
        current: progress.value.current,
        total: progress.value.total,
      })
    case 'packing':
      return t('epubRotate.stagePacking')
    case 'done':
      return t('epubRotate.stageDone')
    default:
      return ''
  }
})

const targetLabel = computed(() => {
  if (!result.value) return ''
  return result.value.target === 'vertical'
    ? t('epubRotate.targetVertical')
    : t('epubRotate.targetHorizontal')
})

const onFileChange = (options: { fileList: UploadFileInfo[] }): void => {
  fileList.value = options.fileList.slice(-1)
  const file = fileList.value[0]?.file ?? null
  selectedFile.value = file
  if (result.value || error.value) reset()
  if (file && !file.name.toLowerCase().endsWith('.epub')) {
    message.error(t('epubRotate.errors.notEpub'))
    selectedFile.value = null
    fileList.value = []
  }
}

const onConvert = async (): Promise<void> => {
  if (!selectedFile.value) {
    message.error(t('epubRotate.errors.noFile'))
    return
  }
  const res = await convert(selectedFile.value, mode.value)
  if (!res) {
    const key = error.value
    if (key === 'notEpub') message.error(t('epubRotate.errors.notEpub'))
    else if (key === 'noContent') message.error(t('epubRotate.errors.noContent'))
    else if (key === 'parseFailed') message.error(t('epubRotate.errors.parseFailed'))
    else if (key === 'tooLarge') message.error(t('epubRotate.errors.tooLarge'))
    else message.error(t('epubRotate.errors.convertFailed'))
    return
  }
  downloadResult()
  message.success(t('epubRotate.downloadReady'))
}

const onRestart = async (): Promise<void> => {
  if (!selectedFile.value) return
  reset()
  await onConvert()
}

const onSelectNew = (): void => {
  selectedFile.value = null
  fileList.value = []
  reset()
}
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-3">
        <div class="lf-eyebrow">{{ t('nav.tools') }}</div>
        <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
          {{ t('epubRotate.title') }}
        </h1>
        <p class="max-w-3xl text-sm leading-7 text-lf-text-muted">
          {{ t('epubRotate.description') }}
        </p>
      </div>
    </section>

    <section class="lf-panel space-y-5 p-5">
      <div class="space-y-2">
        <p class="text-sm font-medium text-lf-text-strong">{{ t('epubRotate.selectFile') }}</p>
        <p class="text-xs text-lf-text-muted">{{ t('epubRotate.selectFileHint') }}</p>
        <NUpload
          v-model:file-list="fileList"
          accept=".epub,application/epub+zip"
          :max="1"
          :default-upload="false"
          :disabled="busy"
          @change="onFileChange"
        >
          <NUploadDragger>
            <div class="flex flex-col items-center gap-2 py-4">
              <IconifyIcon icon="carbon:document" class="text-3xl text-brand-600" />
              <span class="text-sm text-lf-text">{{ t('epubRotate.selectFile') }}</span>
            </div>
          </NUploadDragger>
        </NUpload>
        <p v-if="selectedFile" class="text-xs text-lf-text-muted">
          {{ t('epubRotate.selectedFile', { name: selectedFile.name }) }}
        </p>
      </div>

      <div class="space-y-2">
        <p class="text-sm font-medium text-lf-text-strong">{{ t('epubRotate.modeLabel') }}</p>
        <p class="text-xs text-lf-text-muted">{{ t('epubRotate.modeHint') }}</p>
        <NRadioGroup v-model:value="mode" name="epub-rotate-mode" :disabled="busy">
          <NSpace>
            <NRadio
              v-for="opt in modeOptions"
              :key="opt.value"
              :value="opt.value"
              :label="opt.label"
            />
          </NSpace>
        </NRadioGroup>
      </div>

      <div
        class="rounded-xl border border-lf-border-soft bg-lf-surface-muted px-3.5 py-3 text-xs leading-5 text-lf-text-muted"
      >
        {{ t('epubRotate.privacyNote') }}
      </div>

      <div class="flex flex-wrap items-center gap-3">
        <NButton
          type="primary"
          :loading="busy"
          :disabled="!selectedFile || busy"
          @click="onConvert"
        >
          <template #icon>
            <IconifyIcon icon="carbon:text-vertical-alignment" class="text-base" />
          </template>
          {{ busy ? t('epubRotate.processing') : t('epubRotate.convert') }}
        </NButton>
        <NButton v-if="result" quaternary :disabled="busy" @click="onRestart">
          {{ t('epubRotate.restart') }}
        </NButton>
        <NButton v-if="selectedFile || result" quaternary :disabled="busy" @click="onSelectNew">
          {{ t('epubRotate.selectNew') }}
        </NButton>
        <NButton v-if="result" secondary :disabled="busy" @click="downloadResult">
          <template #icon>
            <IconifyIcon icon="carbon:download" class="text-base" />
          </template>
          {{ t('epubRotate.download') }}
        </NButton>
      </div>

      <div v-if="busy || stage === 'done'" class="space-y-2">
        <div class="flex items-center justify-between text-xs text-lf-text-muted">
          <span>{{ stageLabel }}</span>
          <span v-if="busy"><NSpin :size="14" /></span>
        </div>
        <NProgress
          type="line"
          :percentage="progressPercent"
          :indicator-placement="'inside'"
          :processing="busy"
        />
      </div>

      <div
        v-if="result"
        class="rounded-xl border border-lf-border-soft bg-lf-surface-muted px-3.5 py-3 text-sm text-lf-text"
      >
        <p class="font-medium text-lf-text-strong">{{ t('epubRotate.downloadReady') }}</p>
        <p class="mt-1 text-xs leading-5 text-lf-text-muted">
          {{ t('epubRotate.unifiedTarget', { target: targetLabel }) }}
        </p>
        <p class="mt-1 text-xs leading-5 text-lf-text-muted">
          {{
            t('epubRotate.stats', {
              content: result.stats.content,
              styles: result.stats.styles,
              changed: result.stats.changed,
              already: result.stats.alreadyTarget,
              skipped: result.stats.skipped,
            })
          }}
        </p>
        <p class="mt-1 text-xs text-lf-text-muted">
          {{
            t('epubRotate.detectResult', {
              vertical: result.stats.detectedVertical,
              horizontal: result.stats.detectedHorizontal,
            })
          }}
        </p>
      </div>
    </section>
  </div>
</template>
