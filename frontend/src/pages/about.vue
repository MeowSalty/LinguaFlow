<template>
  <main class="page">
    <NCard title="关于文件式路由">
      <p>Vue Router 会扫描 <code>src/pages</code> 目录，并根据文件结构生成路由记录。</p>
      <p>当前页面来自 <code>src/pages/about.vue</code>，路径会自动映射为 <code>/about</code>。</p>
    </NCard>
  </main>
</template>

<style scoped>
.page {
  max-width: 960px;
  margin: 0 auto;
}
</style>
