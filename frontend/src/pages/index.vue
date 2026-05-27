<script setup lang="ts">
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()

const greeting = computed(() => {
  const name = auth.user?.display_name?.trim() || auth.user?.username
  return name ? `欢迎回来,${name}` : '欢迎使用 LinguaFlow'
})
</script>

<template>
  <div class="space-y-6">
    <NCard :bordered="false" class="shadow-sm shadow-slate-200/60">
      <div class="flex flex-col gap-2">
        <h1 class="text-2xl font-semibold tracking-tight text-slate-900">
          {{ greeting }}
        </h1>
        <p class="text-sm text-slate-500">
          这是 LinguaFlow 的工作台首页。后续会在此展示翻译任务、用量统计与活动记录。
        </p>
      </div>
    </NCard>

    <div class="grid grid-cols-1 gap-6 md:grid-cols-3">
      <NCard title="账户信息" :bordered="false" class="shadow-sm shadow-slate-200/60">
        <dl class="space-y-2 text-sm">
          <div class="flex items-center justify-between">
            <dt class="text-slate-500">用户名</dt>
            <dd class="font-medium text-slate-900">{{ auth.user?.username ?? '-' }}</dd>
          </div>
          <div class="flex items-center justify-between">
            <dt class="text-slate-500">邮箱</dt>
            <dd class="font-medium text-slate-900">{{ auth.user?.email ?? '-' }}</dd>
          </div>
          <div class="flex items-center justify-between">
            <dt class="text-slate-500">角色</dt>
            <dd>
              <NTag size="small" :bordered="false">{{ auth.user?.role ?? '-' }}</NTag>
            </dd>
          </div>
        </dl>
      </NCard>

      <NCard title="快速操作" :bordered="false" class="shadow-sm shadow-slate-200/60">
        <p class="text-sm text-slate-500">导入文档,即可发起一个新的翻译任务。</p>
        <NButton type="primary" class="mt-4" disabled>新建翻译任务</NButton>
      </NCard>

      <NCard title="提示" :bordered="false" class="shadow-sm shadow-slate-200/60">
        <p class="text-sm text-slate-500">
          需要更换连接的 LinguaFlow 服务器,请点击右上角头像菜单中的「切换服务器」。
        </p>
      </NCard>
    </div>
  </div>
</template>
