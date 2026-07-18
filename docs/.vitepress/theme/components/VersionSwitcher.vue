<script setup lang="ts">
import { ref, onMounted } from 'vue'

const versions = ref<string[]>([])
const currentVersion = ref('')
const loaded = ref(false)

onMounted(async () => {
  try {
    const base = import.meta.env.BASE_URL || '/'
    const res = await fetch(`${base}versions.json`)
    if (res.ok) {
      const data = await res.json()
      versions.value = data.versions || []
    }
  } catch {
    versions.value = []
  }

  // 根据当前 URL 判断版本
  const path = window.location.pathname
  const base = import.meta.env.BASE_URL || '/'
  const relativePath = path.replace(base, '').replace(/\/$/, '')

  if (relativePath === 'next') {
    currentVersion.value = 'next'
  } else {
    const matched = versions.value.find(v => relativePath === v || relativePath.startsWith(`${v}/`))
    currentVersion.value = matched || versions.value[0] || ''
  }

  loaded.value = true
})

function onVersionChange(event: Event) {
  const target = event.target as HTMLSelectElement
  const version = target.value
  const base = import.meta.env.BASE_URL || '/'
  if (version === versions.value[0]) {
    // 最新版本跳转到根路径
    window.location.href = base
  } else {
    window.location.href = `${base}${version}/`
  }
}
</script>

<template>
  <div v-if="loaded && versions.length > 0" class="version-switcher">
    <select :value="currentVersion" @change="onVersionChange">
      <option value="next">next</option>
      <option v-for="v in versions" :key="v" :value="v">{{ v }}</option>
    </select>
  </div>
</template>

<style scoped>
.version-switcher {
  display: inline-flex;
  align-items: center;
  margin-left: 12px;
}

.version-switcher select {
  appearance: none;
  background-color: var(--vp-c-bg-soft);
  border: 1px solid var(--vp-c-border);
  border-radius: 8px;
  padding: 4px 28px 4px 12px;
  font-size: 14px;
  color: var(--vp-c-text-1);
  cursor: pointer;
  outline: none;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='12' height='12' viewBox='0 0 12 12'%3E%3Cpath fill='%23666' d='M6 8L1 3h10z'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 8px center;
}

.version-switcher select:hover {
  border-color: var(--vp-c-brand-1);
}

.version-switcher select:focus {
  border-color: var(--vp-c-brand-1);
  box-shadow: 0 0 0 2px var(--vp-c-brand-soft);
}
</style>
