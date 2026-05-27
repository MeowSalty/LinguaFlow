<template>
  <main class="page">
    <NCard title="欢迎使用 LinguaFlow" class="hero-card">
      <p>这是由 Vue Router 文件式路由自动生成的首页。</p>
      <p>当前页面来自 <code>src/pages/index.vue</code>，路径会自动映射为 <code>/</code>。</p>
      <template #action>
        <RouterLink to="/about">查看关于页面</RouterLink>
      </template>
    </NCard>
  </main>
</template>

<style scoped>
.page {
  max-width: 960px;
  margin: 0 auto;
}

.hero-card {
  text-align: center;
}
</style>
