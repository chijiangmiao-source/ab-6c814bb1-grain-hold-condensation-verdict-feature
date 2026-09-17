<script setup>
import { computed, nextTick, onMounted, reactive, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { createAssessment, createAssessmentBatch, listAssessments } from '@/lib/api.js'
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

// Bulk entry before berth: the chief officer switches the same page to batch
// mode and records up to MAX_BATCH_ROWS measurements in measurement order.
const MAX_BATCH_ROWS = 20
const NUM_FIELDS = [
  { key: 'tg', label: '粮温 Tg', min: -20, max: 60 },
  { key: 'ta', label: '舱内气温 Ta', min: -20, max: 60 },
  { key: 'rh', label: '相对湿度 RH', min: 1, max: 100 },
]

const form = reactive({ voyage: '', hatch: '', tg: '', ta: '', rh: '' })
const errors = reactive({})
const submitError = ref('')
const submitting = ref(false)
const latest = ref(null)
const items = ref([])

// ---- Batch mode state -----------------------------------------------------
const mode = ref('single') // single | batch
const batchRows = ref([emptyBatchRow()])
// batchErrors[rowIndex][field] = { field, code, message }
const batchErrors = reactive({})
const batchSubmitError = ref('')
const batchResult = ref(null)

function emptyBatchRow(copyVoyage = '') {
  return { voyage: copyVoyage, hatch: '', tg: '', ta: '', rh: '' }
}

function switchMode(next) {
  mode.value = next
}

function addBatchRow() {
  if (batchRows.value.length >= MAX_BATCH_ROWS) return
  // Multi-hatch readings usually share one voyage: carry the last row's
  // voyage forward; every other value stays blank.
  const prevVoyage = batchRows.value[batchRows.value.length - 1]?.voyage ?? ''
  batchRows.value.push(emptyBatchRow(prevVoyage))
}

function removeBatchRow(i) {
  if (batchRows.value.length === 1) return
  batchRows.value.splice(i, 1)
  delete batchErrors[i]
}

function batchRowInvalid(i) {
  return !!batchErrors[i] && Object.keys(batchErrors[i]).length > 0
}

function batchErrFor(i, key) {
  return batchErrors[i]?.[key]?.message || ''
}

function clearBatchErrors() {
  for (const k of Object.keys(batchErrors)) delete batchErrors[k]
}

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

// Same per-field rules as single mode, applied independently to every row.
// This only saves a round trip on obvious mistakes; the server re-validates
// all rows and its row-numbered 422 errors are rendered verbatim.
function validateBatchLocally() {
  clearBatchErrors()
  batchRows.value.forEach((row, i) => {
    const fail = (field, message) => {
      if (!batchErrors[i]) batchErrors[i] = {}
      batchErrors[i][field] = { field, code: 'client', message }
    }
    if (!String(row.voyage).trim()) fail('voyage', '航次代号不能为空')
    if (!String(row.hatch).trim()) fail('hatch', '舱号不能为空')
    for (const { key, label, min, max } of NUM_FIELDS) {
      const raw = String(row[key]).trim()
      if (raw === '') { fail(key, `${label}必须填写`); continue }
      const v = Number(raw)
      if (!Number.isFinite(v)) { fail(key, `${label}必须为有限数值`); continue }
      if (v < min || v > max) fail(key, `${label}必须在 ${min.toFixed(1)} 至 ${max.toFixed(1)} 之间`)
    }
  })
  return Object.keys(batchErrors).length === 0
}

// Focus/scroll the first invalid row so the chief officer can fix it without
// hunting through twenty lines; all entered values stay in place.
function locateFirstInvalidBatchRow() {
  return nextTick(() => {
    const firstIdx = batchRows.value.findIndex((_, i) => batchRowInvalid(i))
    if (firstIdx < 0) return
    const el = document.getElementById(`batch-row-${firstIdx}`)
    if (el && typeof el.scrollIntoView === 'function') {
      el.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }
    const firstBadField = ['voyage', 'hatch', 'tg', 'ta', 'rh']
      .find((k) => batchErrors[firstIdx]?.[k])
    el?.querySelector(`#bf-${firstIdx}-${firstBadField}`)?.focus?.()
  })
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

async function submitBatch() {
  batchSubmitError.value = ''
  batchResult.value = null
  if (!validateBatchLocally()) {
    await locateFirstInvalidBatchRow()
    return
  }

  const payload = batchRows.value.map((row) => ({
    voyage: String(row.voyage).trim(),
    hatch: String(row.hatch).trim(),
    tg: Number(row.tg),
    ta: Number(row.ta),
    rh: Number(row.rh),
  }))

  submitting.value = true
  try {
    const res = await createAssessmentBatch(payload)
    if (res.status === 422) {
      // Whole batch rejected, nothing saved. Apply the server's row-numbered
      // field errors; every input stays exactly as entered.
      clearBatchErrors()
      for (const f of res.data.fields || []) {
        const idx = Number(f.row) - 1
        if (!batchErrors[idx]) batchErrors[idx] = {}
        batchErrors[idx][f.field] = f
      }
      batchSubmitError.value = res.data.error || '批量输入校验失败，整批未保存'
      await locateFirstInvalidBatchRow()
      return
    }
    if (!res.ok) {
      batchSubmitError.value = res.data?.error || `请求失败（${res.status}）`
      return
    }
    // One ordered result per row: id, unrounded/display delta and verdict,
    // each linking to the existing detail page.
    batchResult.value = Array.isArray(res.data?.items) ? res.data.items : []
    await refreshList()
  } catch (e) {
    batchSubmitError.value = '无法连接 API：' + e.message
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
    <div class="card form">
      <div class="mode-switch" role="tablist" aria-label="录入模式">
        <button
          type="button"
          role="tab"
          :aria-selected="mode === 'single'"
          :class="{ active: mode === 'single' }"
          @click="switchMode('single')"
        >单条录入</button>
        <button
          type="button"
          role="tab"
          :aria-selected="mode === 'batch'"
          :class="{ active: mode === 'batch' }"
          @click="switchMode('batch')"
        >批量模式（最多 {{ MAX_BATCH_ROWS }} 行）</button>
      </div>

      <!-- ============================ 单条模式 ============================ -->
      <form v-if="mode === 'single'" novalidate @submit.prevent="submit">
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

      <!-- ============================ 批量模式 ============================ -->
      <form v-else class="batch-form" novalidate @submit.prevent="submitBatch">
        <h2>批量抄录测量（靠港前集中测量）</h2>
        <p class="note">按实际测量先后逐行填写（最多 {{ MAX_BATCH_ROWS }} 行），一次提交。
          服务端逐行复用单条校验与露点判定，并在<b>同一个事务</b>中依次保存：
          同航次同舱的后续行会自动关联本批较早记录；任一行非法则整批不落库。</p>

        <div class="batch-table-wrap">
          <table class="batch-table">
            <thead>
              <tr>
                <th class="col-idx">#</th>
                <th>航次</th>
                <th>舱号</th>
                <th>粮温 Tg（℃）</th>
                <th>气温 Ta（℃）</th>
                <th>湿度 RH（%）</th>
                <th class="col-op"></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(row, i) in batchRows"
                :id="`batch-row-${i}`"
                :key="i"
                class="batch-row"
                :class="{ 'row-invalid': batchRowInvalid(i) }"
                data-test="batch-row"
              >
                <td class="col-idx">
                  <span :class="{ 'bad-idx': batchRowInvalid(i) }">{{ i + 1 }}</span>
                </td>
                <td v-for="col in ['voyage', 'hatch', 'tg', 'ta', 'rh']" :key="col">
                  <input
                    :id="`bf-${i}-${col}`"
                    v-model="row[col]"
                    :type="col === 'voyage' || col === 'hatch' ? 'text' : 'number'"
                    :min="col === 'rh' ? 1 : -20"
                    :max="col === 'rh' ? 100 : 60"
                    step="0.1"
                    :aria-invalid="!!batchErrors[i]?.[col]"
                    :aria-describedby="batchErrFor(i, col) ? `berr-${i}-${col}` : null"
                  />
                  <p v-if="batchErrFor(i, col)" :id="`berr-${i}-${col}`" class="field-err">
                    {{ batchErrFor(i, col) }}
                  </p>
                </td>
                <td class="col-op">
                  <button
                    type="button"
                    class="row-del"
                    data-test="batch-remove-row"
                    :disabled="batchRows.length === 1"
                    title="删除本行"
                    @click="removeBatchRow(i)"
                  >✕</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <button type="button" class="add-row" data-test="batch-add-row"
          :disabled="batchRows.length >= MAX_BATCH_ROWS" @click="addBatchRow">
          + 添加一行（{{ batchRows.length }}/{{ MAX_BATCH_ROWS }}）
        </button>

        <p v-if="batchSubmitError" class="banner-error" data-test="batch-error">
          {{ batchSubmitError }}
          <span v-if="Object.keys(batchErrors).length" class="locate-hint">
            已定位到首个问题行（红底标记），全部输入均已保留。
          </span>
        </p>

        <button type="submit" class="batch-submit" :disabled="submitting">
          {{ submitting ? '批量复算保存中…' : '一次提交整批评估' }}
        </button>
      </form>
    </div>

    <div class="side">
      <template v-if="mode === 'single'">
      <div v-if="latest" class="card result" data-test="single-result">
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
      </template>

      <!-- ===================== 批量提交结果 ===================== -->
      <div v-else-if="batchResult" class="card result" data-test="batch-result">
        <h2>本批 {{ batchResult.length }} 行评估结果</h2>
        <p class="note">按提交顺序排列；编号、未舍入温差与结论均来自 Go API，点击编号进入既有详情页。</p>
        <table class="batch-result-table">
          <thead>
            <tr><th>行</th><th>评估编号</th><th>航次/舱号</th><th>Δ（未舍入）</th><th>Δ（展示）</th><th>结论</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="(a, i) in batchResult" :key="a.id" data-test="batch-result-row">
              <td>{{ i + 1 }}</td>
              <td><RouterLink :to="`/assessments/${a.id}`" class="link">#{{ a.id }}</RouterLink></td>
              <td>{{ a.voyage }} / {{ a.hatch }}</td>
              <td>{{ a.delta }}</td>
              <td class="strong">{{ fmt2(a.delta_display) }}</td>
              <td><VerdictBadge :verdict="a.verdict" :hint="false" /></td>
              <td><RouterLink :to="`/assessments/${a.id}`" class="link">详情 →</RouterLink></td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else-if="mode === 'batch'" class="card placeholder" data-test="batch-placeholder">
        <h2>本批评估结果</h2>
        <p>填写并提交后，在此逐行显示评估编号、未舍入温差 Δ 与结论；任一行非法时整批不会落库。</p>
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
