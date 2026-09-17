<script setup>
import { RouterLink, RouterView } from 'vue-router'
</script>

<template>
  <div class="layout">
    <header>
      <RouterLink to="/" class="brand">⚓ 散粮船通风露点判定</RouterLink>
      <span class="sub">Magnus 露点法 · 结论以 Go API 未舍入 Δ 为准</span>
    </header>
    <main>
      <RouterView />
    </main>
    <footer>
      判定口径：γ = ln(RH/100) + 17.62·Ta/(243.12+Ta)，Td = 243.12·γ/(17.62−γ)，Δ = Tg − Td（未舍入）。
    </footer>
  </div>
</template>
