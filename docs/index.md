---
layout: page
---

---
layout: page
---

<script setup>
import { onMounted } from 'vue'
import { useRouter, withBase } from 'vitepress'

const router = useRouter()
onMounted(() => {
  location.replace(withBase('/zh/'))
})
</script>

<noscript>
  <meta http-equiv="refresh" content="0; url=./zh/" />
</noscript>

正在跳转...
