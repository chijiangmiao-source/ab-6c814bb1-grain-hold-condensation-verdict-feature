<script setup>
defineProps({
  verdict: { type: String, default: '' },
  hint: { type: Boolean, default: true },
})

const VERDICTS = {
  allowed: { text: '允许通风', cls: 'v-allowed', hint: 'Δ > 2.00，粮温显著高于露点，可开启舱口通风' },
  denied: { text: '禁止通风', cls: 'v-denied', hint: 'Δ < −2.00，湿空气会在冷粮表面结露，严禁通风' },
  retest: { text: '暂停并复测', cls: 'v-retest', hint: '−2.00 ≤ Δ ≤ 2.00（含两端点），临界区间，暂缓操作并重新测量' },
}
function info(v) {
  return VERDICTS[v] ?? { text: String(v ?? '—'), cls: 'v-unknown', hint: '' }
}
</script>

<template>
  <span class="verdict" :class="info(verdict).cls">
    <strong>{{ info(verdict).text }}</strong>
    <small v-if="hint && info(verdict).hint">（{{ info(verdict).hint }}）</small>
  </span>
</template>
