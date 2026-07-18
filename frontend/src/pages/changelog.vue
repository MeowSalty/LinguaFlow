<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

interface GitHubRelease {
  id: number
  tag_name: string
  name: string
  body: string
  published_at: string
  html_url: string
  prerelease: boolean
}

const releases = ref<GitHubRelease[]>([])
const loading = ref(true)
const error = ref<string | null>(null)

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString('zh-CN', {
    year: 'numeric',
    month: 'long',
    day: 'numeric',
  })
}

function renderMarkdown(text: string): string {
  return text
    .replace(
      /^### (.+)$/gm,
      '<h4 class="text-sm font-semibold text-lf-text-strong mt-4 mb-2">$1</h4>',
    )
    .replace(
      /^## (.+)$/gm,
      '<h3 class="text-base font-semibold text-lf-text-strong mt-4 mb-2">$1</h3>',
    )
    .replace(
      /^# (.+)$/gm,
      '<h2 class="text-lg font-semibold text-lf-text-strong mt-4 mb-2">$1</h2>',
    )
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/\*(.+?)\*/g, '<em>$1</em>')
    .replace(
      /\[([^\]]+)\]\(([^)]+)\)/g,
      '<a href="$2" target="_blank" rel="noopener" class="text-brand-600 hover:underline">$1</a>',
    )
    .replace(
      /`([^`]+)`/g,
      '<code class="rounded bg-lf-code-bg px-1 py-0.5 font-mono text-xs">$1</code>',
    )
    .replace(/^- (.+)$/gm, '<li class="ml-4 list-disc">$1</li>')
    .replace(/^\* (.+)$/gm, '<li class="ml-4 list-disc">$1</li>')
    .replace(/^\d+\. (.+)$/gm, '<li class="ml-4 list-decimal">$1</li>')
    .replace(/\n\n/g, '</p><p class="mt-2">')
    .replace(/\n/g, '<br>')
}

async function fetchReleases(): Promise<void> {
  loading.value = true
  error.value = null

  try {
    const response = await fetch(
      'https://api.github.com/repos/MeowSalty/LinguaFlow/releases?per_page=20',
    )

    if (!response.ok) {
      throw new Error(`GitHub API error: ${response.status}`)
    }

    const data = (await response.json()) as GitHubRelease[]
    releases.value = data.filter((r) => !r.prerelease)
  } catch (err) {
    error.value = err instanceof Error ? err.message : t('changelog.fetchError')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  fetchReleases()
})
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-3">
        <div class="lf-eyebrow">{{ t('nav.changelog') }}</div>
        <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
          {{ t('changelog.title') }}
        </h1>
        <p class="max-w-2xl text-sm leading-6 text-lf-text-muted">
          {{ t('changelog.description') }}
        </p>
      </div>
    </section>

    <div v-if="loading" class="space-y-4">
      <div v-for="i in 3" :key="i" class="lf-panel p-5">
        <NSkeleton height="96px" :sharp="false" />
      </div>
    </div>

    <div v-else-if="error" class="lf-panel p-8">
      <NResult status="error" :title="t('changelog.fetchError')" :description="error">
        <template #footer>
          <NButton @click="fetchReleases">
            {{ t('changelog.retry') }}
          </NButton>
        </template>
      </NResult>
    </div>

    <div v-else-if="releases.length === 0" class="lf-panel py-16">
      <NEmpty :description="t('changelog.empty')" />
    </div>

    <div v-else class="relative space-y-4 pl-2">
      <div
        class="absolute top-2 bottom-2 left-[11px] w-px bg-gradient-to-b from-brand-500/50 via-lf-border to-transparent"
      />
      <article v-for="release in releases" :key="release.id" class="lf-panel relative ml-6 p-5">
        <span
          class="absolute top-6 -left-[31px] h-2.5 w-2.5 rounded-full border-2 border-brand-500 bg-lf-surface shadow-sm shadow-brand-500/30"
        />
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div class="flex flex-wrap items-center gap-3">
            <NTag type="success" size="small" :bordered="false">
              {{ release.tag_name }}
            </NTag>
            <span class="text-xs text-lf-text-subtle">
              {{ formatDate(release.published_at) }}
            </span>
            <span
              v-if="release.name && release.name !== release.tag_name"
              class="text-sm font-medium text-lf-text-strong"
            >
              {{ release.name }}
            </span>
          </div>
          <NButton
            tag="a"
            :href="release.html_url"
            target="_blank"
            rel="noopener"
            quaternary
            size="small"
          >
            {{ t('changelog.viewOnGithub') }}
          </NButton>
        </div>
        <div
          class="prose-sm mt-4 text-sm leading-relaxed text-lf-text"
          v-html="renderMarkdown(release.body || t('changelog.noDescription'))"
        />
      </article>
    </div>
  </div>
</template>
