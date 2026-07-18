<script setup lang="ts">
import { Icon as IconifyIcon } from '@iconify/vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const formatBadges = [
  { label: 'Markdown', variants: '.md / .markdown / .mdx' },
  { label: 'EPUB', variants: '.epub' },
  { label: 'SRT', variants: '.srt' },
  { label: 'VTT', variants: '.vtt' },
  { label: 'ASS', variants: '.ass' },
  { label: 'TXT', variants: '.txt' },
]

const features = [
  {
    icon: 'carbon:folder',
    title: 'about.featureProject',
    desc: 'about.featureProjectDesc',
  },
  {
    icon: 'carbon:magic-wand',
    title: 'about.featureTranslation',
    desc: 'about.featureTranslationDesc',
  },
  {
    icon: 'carbon:stack',
    title: 'about.featureBatch',
    desc: 'about.featureBatchDesc',
  },
  {
    icon: 'carbon:book',
    title: 'about.featureEpub',
    desc: 'about.featureEpubDesc',
  },
  {
    icon: 'carbon:dictionary',
    title: 'about.featureGlossary',
    desc: 'about.featureGlossaryDesc',
  },
  {
    icon: 'carbon:checkmark-outline',
    title: 'about.featureReview',
    desc: 'about.featureReviewDesc',
  },
  {
    icon: 'carbon:settings',
    title: 'about.featureConfig',
    desc: 'about.featureConfigDesc',
  },
]

const techStack = [
  { layer: 'about.techFrontend', tech: 'Vue 3 + TypeScript + naive-ui + Tailwind CSS' },
  { layer: 'about.techBackend', tech: 'Go + ent + chi' },
  { layer: 'about.techDatabase', tech: 'SQLite' },
  { layer: 'about.techApi', tech: 'OpenAPI 3.0' },
]
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-3">
        <div class="lf-eyebrow">{{ t('nav.about') }}</div>
        <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
          {{ t('about.title') }}
        </h1>
        <p class="max-w-3xl text-sm leading-7 text-lf-text-muted">
          {{ t('about.description') }}
        </p>
      </div>
    </section>

    <section class="lf-panel p-5">
      <h2 class="text-sm font-semibold tracking-wide text-lf-text-strong">
        {{ t('about.supportedFormats') }}
      </h2>
      <div class="mt-4 flex flex-wrap gap-2">
        <div
          v-for="fmt in formatBadges"
          :key="fmt.label"
          class="inline-flex items-center gap-2 rounded-full border border-lf-border-soft bg-lf-surface-muted px-3 py-1.5"
        >
          <span class="text-xs font-semibold text-lf-text-strong">{{ fmt.label }}</span>
          <span class="font-mono text-[11px] text-lf-text-subtle">{{ fmt.variants }}</span>
        </div>
      </div>
    </section>

    <section>
      <h2 class="mb-3 text-sm font-semibold tracking-wide text-lf-text-strong">
        {{ t('about.coreFeatures') }}
      </h2>
      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <div
          v-for="f in features"
          :key="f.title"
          class="lf-panel flex gap-3 p-4 transition-colors hover:border-brand-500/20"
        >
          <div
            class="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-lf-brand-soft text-brand-600"
          >
            <IconifyIcon :icon="f.icon" class="text-base" />
          </div>
          <div class="min-w-0">
            <p class="text-sm font-medium text-lf-text-strong">{{ t(f.title) }}</p>
            <p class="mt-1 text-xs leading-5 text-lf-text-muted">{{ t(f.desc) }}</p>
          </div>
        </div>
      </div>
    </section>

    <section class="grid gap-4 lg:grid-cols-2">
      <div class="lf-panel p-5">
        <h2 class="text-sm font-semibold tracking-wide text-lf-text-strong">
          {{ t('about.techStack') }}
        </h2>
        <div class="mt-4 space-y-3">
          <div
            v-for="item in techStack"
            :key="item.layer"
            class="flex items-start justify-between gap-4 rounded-xl border border-lf-border-soft bg-lf-surface-muted px-3.5 py-3"
          >
            <span class="text-xs font-medium text-lf-text-muted">{{ t(item.layer) }}</span>
            <span class="text-right font-mono text-xs text-lf-text-strong">{{ item.tech }}</span>
          </div>
        </div>
      </div>

      <div class="lf-panel p-5">
        <h2 class="text-sm font-semibold tracking-wide text-lf-text-strong">
          {{ t('about.license') }}
        </h2>
        <p class="mt-4 text-sm leading-6 text-lf-text">
          {{ t('about.licenseDesc') }}
        </p>
        <p
          class="mt-3 rounded-xl border border-lf-border-soft bg-lf-code-bg px-3.5 py-3 text-sm text-lf-text-muted"
        >
          {{ t('about.licenseAgpl') }}
        </p>
      </div>
    </section>
  </div>
</template>
