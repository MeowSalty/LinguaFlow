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
    .replace(/^### (.+)$/gm, '<h4 class="text-sm font-semibold text-lf-text mt-4 mb-2">$1</h4>')
    .replace(/^## (.+)$/gm, '<h3 class="text-base font-semibold text-lf-text mt-4 mb-2">$1</h3>')
    .replace(/^# (.+)$/gm, '<h2 class="text-lg font-bold text-lf-text mt-4 mb-2">$1</h2>')
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/\*(.+?)\*/g, '<em>$1</em>')
    .replace(
      /\[([^\]]+)\]\(([^)]+)\)/g,
      '<a href="$2" target="_blank" rel="noopener" class="text-lf-primary hover:underline">$1</a>',
    )
    .replace(/`([^`]+)`/g, '<code class="rounded bg-lf-code-bg px-1 py-0.5 text-xs">$1</code>')
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
  <div class="space-y-6">
    <NCard :title="t('changelog.title')" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <p class="text-sm text-lf-text-muted">
        {{ t('changelog.description') }}
      </p>
    </NCard>

    <NCard v-if="loading" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="space-y-4">
        <NSkeleton v-for="i in 3" :key="i" height="120px" :sharp="false" />
      </div>
    </NCard>

    <NCard v-else-if="error" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <NResult status="error" :title="t('changelog.fetchError')" :description="error">
        <template #footer>
          <NButton @click="fetchReleases">
            {{ t('changelog.retry') }}
          </NButton>
        </template>
      </NResult>
    </NCard>

    <NCard v-else-if="releases.length === 0" :bordered="false" class="shadow-sm shadow-lf-shadow">
      <NEmpty :description="t('changelog.empty')" />
    </NCard>

    <template v-else>
      <NCard
        v-for="release in releases"
        :key="release.id"
        :bordered="false"
        class="shadow-sm shadow-lf-shadow"
      >
        <template #header>
          <div class="flex items-center gap-3">
            <NTag type="success" size="small">
              {{ release.tag_name }}
            </NTag>
            <span class="text-sm text-lf-text-muted">
              {{ formatDate(release.published_at) }}
            </span>
          </div>
        </template>

        <template #header-extra>
          <NButton
            tag="a"
            :href="release.html_url"
            target="_blank"
            rel="noopener"
            quaternary
            size="small"
          >
            <template #icon>
              <div class="i-carbon-link" />
            </template>
            {{ t('changelog.viewOnGithub') }}
          </NButton>
        </template>

        <div
          class="prose-sm text-sm text-lf-text leading-relaxed"
          v-html="renderMarkdown(release.body || t('changelog.noDescription'))"
        />
      </NCard>
    </template>
  </div>
</template>
