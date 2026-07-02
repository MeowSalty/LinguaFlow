<script setup lang="ts">
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
    icon: 'i-lucide-folder-kanban',
    title: 'about.featureProject',
    desc: 'about.featureProjectDesc',
  },
  {
    icon: 'i-lucide-sparkles',
    title: 'about.featureTranslation',
    desc: 'about.featureTranslationDesc',
  },
  { icon: 'i-lucide-layers', title: 'about.featureBatch', desc: 'about.featureBatchDesc' },
  { icon: 'i-lucide-book-open', title: 'about.featureEpub', desc: 'about.featureEpubDesc' },
  { icon: 'i-lucide-book-text', title: 'about.featureGlossary', desc: 'about.featureGlossaryDesc' },
  { icon: 'i-lucide-check-circle', title: 'about.featureReview', desc: 'about.featureReviewDesc' },
  { icon: 'i-lucide-settings', title: 'about.featureConfig', desc: 'about.featureConfigDesc' },
]

const techStack = [
  { layer: 'about.techFrontend', tech: 'Vue 3 + TypeScript + naive-ui + Tailwind CSS' },
  { layer: 'about.techBackend', tech: 'Go + ent + chi' },
  { layer: 'about.techDatabase', tech: 'SQLite' },
  { layer: 'about.techApi', tech: 'OpenAPI 3.0' },
]
</script>

<template>
  <div class="space-y-6">
    <NCard :title="t('about.title')" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <p class="text-base text-lf-text leading-relaxed">
        {{ t('about.description') }}
      </p>
    </NCard>

    <NCard
      :title="t('about.supportedFormats')"
      :bordered="false"
      class="shadow-sm shadow-lf-shadow"
    >
      <div class="flex flex-wrap gap-2">
        <NTag v-for="fmt in formatBadges" :key="fmt.label" type="info" size="small">
          {{ fmt.label }}
          <span class="ml-1 text-xs text-lf-text-muted">{{ fmt.variants }}</span>
        </NTag>
      </div>
    </NCard>

    <NCard :title="t('about.coreFeatures')" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="grid gap-4 sm:grid-cols-2">
        <div v-for="f in features" :key="f.title" class="flex gap-3">
          <div :class="f.icon" class="mt-0.5 h-5 w-5 shrink-0 text-lf-primary" />
          <div>
            <p class="text-sm font-medium text-lf-text">{{ t(f.title) }}</p>
            <p class="text-xs text-lf-text-muted">{{ t(f.desc) }}</p>
          </div>
        </div>
      </div>
    </NCard>

    <NCard :title="t('about.techStack')" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <NDescriptions :column="1" label-placement="left" bordered size="small">
        <NDescriptionsItem v-for="item in techStack" :key="item.layer" :label="t(item.layer)">
          {{ item.tech }}
        </NDescriptionsItem>
      </NDescriptions>
    </NCard>

    <NCard :title="t('about.license')" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <p class="text-sm text-lf-text">
        {{ t('about.licenseDesc') }}
      </p>
      <p class="mt-2 text-sm text-lf-text-muted">
        {{ t('about.licenseAgpl') }}
      </p>
    </NCard>
  </div>
</template>
