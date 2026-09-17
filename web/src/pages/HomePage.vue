<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { createAssessment, listAssessments } from '@/lib/api.js'
import VerdictBadge from '@/components/VerdictBadge.vue'

// Constraints mirror decision.Validate on the server. Client-side limits
// give instant feedback; the server remains the authority and its 422
// field errors are rendered verbatim.
const FIELDS = [
  { key: 'voyage', label: '航次代号', type: 'text', placeholder: '如 V-2026-09' },
  { key: 'hatch', label: '舱号', type: 'text', placeholder: '如 3H' },
  { key: 'tg', label: '粮温 Tg（℃）', type: 'number', min: -20, max: 60, step: '0.1', placeholder: '-20.0 ~ 60.0' },
  { key: 'ta', label: '舱内气温 Ta（℃）', type: 'number', min: -20, max: 60, step: '0.1', placeholder: '-20.0 ~ 60.0' },
  { key: 'rh', label: '相对湿度 RH（%）', type: 'number', min: 1, max: 100, step: '0.1', placeholder: '1.0 ~ 100.0' },
]

const form = reactive({ voyage: '', hatch: '', tg: '', ta: '', rh: '' })
const errors = reactive({})
const submitError = ref('')
const submitting = ref(false)
const latest = ref(null)
const items = ref([])

function errFor(key) {
  return errors[key]?.message || ''
}

// Instant client-side checks; a rejected value is never sent.
function validateLocally() {
  for (const k of Object.keys(errors)) delete errors[k]
  const fail = (key, message) => { errors[key] = { field: key, code: 'client', message } }

  if (!form.voyage.trim()) fail('voyage', '航次代号不能为空')
  if (!form.hatch.trim()) fail('hatch', '舱号不能为空')

  const checkNum = (key, label, lo, hi) => {
    const raw = String(form[key]).trim()
    if (raw === '') { fail(key, `${label}必须填写`); return }
    const v = Number(raw)
    if (!Number.isFinite(v)) { fail(key, `${label}必须为有限数值`); return }
    if (v < lo || v > hi) fail(key, `${label}必须在 ${lo.toFixed(1)} 至 ${hi.toFixed(1)} 之间`)
  }
  checkNum('tg', '粮温 Tg', -20, 60)
  checkNum('ta', '舱内气温 Ta', -20, 60)
  checkNum('rh', '相对湿度 RH', 1, 100)

  return Object.keys(errors).length === 0
}

async function refreshList() {
  try {
    const res = await listAssessments()
    if (res.ok) items.value = res.data.items
  } catch {
    // The form stays usable; the submit action will surface a connect error.
  }
}

async function submit() {
  submitError.value = ''
  latest.value = null
  if (!validateLocally()) return

  submitting.value = true
  try {
    const payload = {
      voyage: form.voyage.trim(),
      hatch: form.hatch.trim(),
      tg: Number(form.tg),
      ta: Number(form.ta),
      rh: Number(form.rh),
    }
    const res = await createAssessment(payload)
    if (res.status === 422) {
      // Server field errors win; merge with any client-side keys.
      for (const f of res.data.fields || []) errors[f.field] = f
      submitError.value = res.data.error || '输入校验失败，未生成记录'
      return
    }
    if (!res.ok) {
      submitError.value = res.data?.error || `请求失败（${res.status}）`
      return
    }
    latest.value = res.data
    await refreshList()
  } catch (e) {
    submitError.value = '无法连接 API：' + e.message
  } finally {
    submitting.value = false
  }
}

const fmt2 = (v) => (v === null || v === undefined ? '—' : Number(v).toFixed(2))

// Distinct voyage codes for the overview entry points. This is only a
// navigation index built from the list; which record is "latest" per hatch is
// decided entirely by the server overview endpoint, never here.
const voyages = computed(() => {
  const seen = new Set()
  const out = []
  for (const a of items.value) {
    if (a.voyage && !seen.has(a.voyage)) {
      seen.add(a.voyage)
      out.push(a.voyage)
    }
  }
  return out
})

onMounted(refreshList)
</script>

<template>
  <section class="grid">
    <form class="card form" novalidate @submit.prevent="submit">
      <h2>录入测量数据</h2>
      <p class="note">温差风险取决于粮温与舱内空气<b>露点</b>之差，而非相对湿度本身。提交后由 Go API 统一复算。</p>

      <div v-for="f in FIELDS" :key="f.key" class="field" :class="{ invalid: !!errors[f.key] }">
        <label :for="'f-' + f.key">{{ f.label }}</label>
        <input
          :id="'f-' + f.key"
          v-model="form[f.key]"
          :type="f.type"
          :min="f.min"
          :max="f.max"
          :step="f.step"
          :placeholder="f.placeholder"
          :aria-invalid="!!errors[f.key]"
          :aria-describedby="errors[f.key] ? 'err-' + f.key : null"
        />
        <p v-if="errFor(f.key)" :id="'err-' + f.key" class="field-err">{{ errFor(f.key) }}</p>
      </div>

      <p v-if="submitError" class="banner-error">{{ submitError }}</p>

      <button type="submit" :disabled="submitting">
        {{ submitting ? '复算中…' : '提交复算' }}
      </button>
    </form>

    <div class="side">
      <div v-if="latest" class="card result">
        <h2>判定结果 <VerdictBadge :verdict="latest.verdict" :hint="false" /></h2>
        <dl>
          <div><dt>γ（未舍入）</dt><dd>{{ latest.gamma }}</dd></div>
          <div><dt>γ（展示）</dt><dd>{{ fmt2(latest.gamma_display) }}</dd></div>
          <div><dt>露点 Td（℃）</dt><dd>{{ fmt2(latest.td_display) }}</dd></div>
          <div><dt>Δ = Tg − Td（℃，未舍入）</dt><dd>{{ latest.delta }}</dd></div>
          <div><dt>Δ（展示，℃）</dt><dd class="strong">{{ fmt2(latest.delta_display) }}</dd></div>
        </dl>
        <RouterLink :to="`/assessments/${latest.id}`" class="link">查看公式代入明细 →</RouterLink>
      </div>
      <div v-else class="card placeholder">
        <h2>判定结果</h2>
        <p>提交一次合法测量后在此显示。Δ&gt;2.00 允许，Δ&lt;−2.00 禁止，闭区间 [−2.00, 2.00] 复测。</p>
      </div>

      <div class="card history">
        <h2>历史记录</h2>
        <p v-if="items.length === 0" class="note">暂无记录。</p>
        <template v-else>
          <div class="voyage-entries" data-test="voyage-entries">
            <span class="note">按航次查看舱位概览（每舱最新风险）：</span>
            <RouterLink
              v-for="v in voyages"
              :key="v"
              :to="`/voyages/${encodeURIComponent(v)}/hatches/latest`"
              class="voyage-chip"
              data-test="voyage-entry"
            >{{ v }} →</RouterLink>
          </div>
          <table>
            <thead>
              <tr><th>#</th><th>航次/舱号</th><th>Tg</th><th>Ta</th><th>RH</th><th>Δ</th><th>结论</th></tr>
            </thead>
            <tbody>
              <tr v-for="a in items" :key="a.id">
                <td><RouterLink :to="`/assessments/${a.id}`" class="link">{{ a.id }}</RouterLink></td>
                <td>{{ a.voyage }} / {{ a.hatch }}</td>
                <td>{{ fmt2(a.tg) }}</td>
                <td>{{ fmt2(a.ta) }}</td>
                <td>{{ fmt2(a.rh) }}</td>
                <td :class="{ strong: true }">{{ fmt2(a.delta_display) }}</td>
                <td><VerdictBadge :verdict="a.verdict" :hint="false" /></td>
              </tr>
            </tbody>
          </table>
          <p class="note">刷新页面后结论仍来自 SQLite 中保存的记录，页面不做二次复算。</p>
        </template>
      </div>
    </div>
  </section>
</template>
