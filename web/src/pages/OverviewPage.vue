<script setup>
import { onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { getVoyageOverview } from '@/lib/api.js'
import VerdictBadge from '@/components/VerdictBadge.vue'

const props = defineProps({ voyage: { type: String, required: true } })

// The overview renders ONLY values the API grouped (one MAX(id) row per
// hatch). The page never compares rows or picks a "latest" itself; a wrong
// server selection would surface verbatim here.
const items = ref([])
const echoedVoyage = ref('')
const status = ref('loading') // loading | ready | error
const errorMessage = ref('')

const fmt2 = (v) => (v === null || v === undefined ? '—' : Number(v).toFixed(2))
const fmtTime = (v) => (v ? new Date(v).toLocaleString() : '—')

async function load(voyage) {
  status.value = 'loading'
  errorMessage.value = ''
  try {
    const res = await getVoyageOverview(voyage)
    if (!res.ok) {
      // A 400 (e.g. malformed path encoding) is an explicit request error;
      // surface the server's message and keep the way back to history.
      status.value = 'error'
      errorMessage.value = res.data?.error || `请求失败（${res.status}）`
      return
    }
    echoedVoyage.value = res.data?.voyage ?? voyage
    items.value = Array.isArray(res.data?.items) ? res.data.items : []
    status.value = 'ready'
  } catch (e) {
    status.value = 'error'
    errorMessage.value = '无法连接 API：' + e.message
  }
}

onMounted(() => load(props.voyage))
watch(() => props.voyage, (v) => load(v))
</script>

<template>
  <section>
    <p><RouterLink to="/" class="link">← 返回历史区</RouterLink></p>

    <div v-if="status === 'loading'" class="card"><p>加载中…</p></div>

    <div v-else-if="status === 'error'" class="card" data-test="overview-error">
      <h2>舱位概览无法加载</h2>
      <p class="banner-error">{{ errorMessage }}</p>
      <p class="note">请检查航次链接是否正确，或返回历史区重新进入。</p>
      <p><RouterLink to="/" class="link">← 返回历史区</RouterLink></p>
    </div>

    <div v-else class="card overview" data-test="overview">
      <h2>
        舱位概览 · 航次 {{ echoedVoyage }}
      </h2>
      <p class="note">每个舱位仅显示该航次的<b>最新一次</b>评估（服务端按记录编号选取），
        测量时间、温差与结论均直接取自接口返回；点击任一行进入该记录详情。</p>

      <p v-if="items.length === 0" class="note" data-test="overview-empty">
        该航次暂无任何舱位记录。
      </p>

      <table v-else data-test="overview-table">
        <thead>
          <tr>
            <th>舱号</th><th>最新评估</th><th>测量时间</th>
            <th>Δ = Tg − Td（℃，未舍入）</th><th>Δ（展示，℃）</th><th>结论</th><th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="a in items" :key="a.id" data-test="overview-row">
            <td class="strong">{{ a.hatch }}</td>
            <td>#{{ a.id }}</td>
            <td>{{ fmtTime(a.created_at) }}</td>
            <td>{{ a.delta }}</td>
            <td>{{ fmt2(a.delta_display) }}</td>
            <td><VerdictBadge :verdict="a.verdict" :hint="false" /></td>
            <td><RouterLink :to="`/assessments/${a.id}`" class="link">查看详情 →</RouterLink></td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>
