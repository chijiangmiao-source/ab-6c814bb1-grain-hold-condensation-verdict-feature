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

// 靠港前可在“批量抄录”模式一次提交最多二十行；顺序即测量先后顺序。
const MAX_BATCH_ROWS = 20

const mode = ref('single') // single | batch

function blankRow() {
  return { voyage: '', hatch: '', tg: '', ta: '', rh: '' }
}

// ---- single-row mode state ----
const form = reactive(blankRow())
const errors = reactive({})
const submitError = ref('')
const submitting = ref(false)
const latest = ref(null)

// ---- batch mode state ----
const batchRows = ref(Array.from({ length: 5 }, blankRow))
// Keyed "rowIndex.field" (0-based index) -> server/client field error.
const batchErrors = reactive({})
const batchSubmitError = ref('')
const batchSubmitting = ref(false)
const batchResults = ref(null)

const items = ref([])

function errFor(key) {
  return errors[key]?.message || ''
}

function batchErrFor(index, key) {
  return batchErrors[`${index}.${key}`]?.message || ''
}

const batchProblemRows = computed(() => {
  const set = new Set()
  for (const k of Object.keys(batchErrors)) set.add(Number(k.split('.')[0]))
  return [...set].sort((a, b) => a - b)
})

const filledBatchRows = computed(() =>
  batchRows.value
    .map((row, index) => ({ row, index }))
    .filter(({ row }) => Object.values(row).some((v) => String(v).trim() !== '')))

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

// Validates every non-empty batch row. Completely empty rows are skipped
// (they are just unused lines); a partially filled row gets a "必须填写"
// error for each blank cell. Returns the parsed payload rows together with
// each payload row's VISUAL row index, so server row numbers (which index
// the submitted array) can always be mapped back to the right table row.
function validateBatchLocally() {
  for (const k of Object.keys(batchErrors)) delete batchErrors[k]
  const fail = (index, key, message) => {
    batchErrors[`${index}.${key}`] = { field: key, code: 'client', message }
  }

  const payload = []
  const indices = []
  for (const { row, index } of filledBatchRows.value) {
    if (!row.voyage.trim()) fail(index, 'voyage', '航次代号不能为空')
    if (!row.hatch.trim()) fail(index, 'hatch', '舱号不能为空')

    const checkNum = (key, label, lo, hi) => {
      const raw = String(row[key]).trim()
      if (raw === '') { fail(index, key, `${label}必须填写`); return }
      const v = Number(raw)
      if (!Number.isFinite(v)) { fail(index, key, `${label}必须为有限数值`); return }
      if (v < lo || v > hi) {
        fail(index, key, `${label}必须在 ${lo.toFixed(1)} 至 ${hi.toFixed(1)} 之间`)
      }
    }
    checkNum('tg', '粮温 Tg', -20, 60)
    checkNum('ta', '舱内气温 Ta', -20, 60)
    checkNum('rh', '相对湿度 RH', 1, 100)

    payload.push({
      voyage: row.voyage.trim(),
      hatch: row.hatch.trim(),
      tg: Number(row.tg),
      ta: Number(row.ta),
      rh: Number(row.rh),
    })
    indices.push(index)
  }

  if (payload.length === 0) {
    batchSubmitError.value = '请至少填写一行测量数据'
    return null
  }
  return { payload, indices }
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

// Focus/scroll to the first row the server rejected, keeping all typed
// values in place so the chief officer can fix and resubmit.
async function locateFirstBatchProblem() {
  await nextTick()
  const first = batchProblemRows.value[0]
  if (first === undefined) return
  const tr = document.getElementById(`batch-row-${first}`)
  if (tr && typeof tr.scrollIntoView === 'function') {
    tr.scrollIntoView({ behavior: 'smooth', block: 'center' })
  }
  const key = Object.keys(batchErrors)
    .filter((k) => Number(k.split('.')[0]) === first)[0]
  const input = key && document.getElementById(`bf-${first}-${key.split('.')[1]}`)
  if (input && typeof input.focus === 'function') input.focus()
}

async function submitBatch() {
  batchSubmitError.value = ''
  batchResults.value = null
  const checked = validateBatchLocally()
  if (!checked) {
    await locateFirstBatchProblem()
    return
  }
  const { payload, indices } = checked
  if (Object.keys(batchErrors).length > 0) {
    batchSubmitError.value = `有 ${batchProblemRows.value.length} 行数据不合法，已定位到第一处问题行；整批未提交、未保存。`
    await locateFirstBatchProblem()
    return
  }

  batchSubmitting.value = true
  try {
    const res = await createAssessmentBatch(payload)
    if (res.status === 422) {
      // Row-numbered field errors from the server: server row n is 1-based
      // into the submitted (non-empty) array; map it to the visual row and
      // keep every typed value so the user can fix in place.
      for (const f of res.data.fields || []) {
        const visualIndex = indices[Number(f.row) - 1]
        if (visualIndex === undefined) continue
        batchErrors[`${visualIndex}.${f.field}`] = f
      }
      const rows = new Set((res.data.fields || []).map((f) => f.row)).size
      batchSubmitError.value =
        (res.data.error || '批量输入校验失败，未生成任何记录') +
        `（共 ${rows} 行存在问题，整批未保存）`
      await locateFirstBatchProblem()
      return
    }
    if (!res.ok) {
      batchSubmitError.value = res.data?.error || `请求失败（${res.status}）`
      return
    }
    batchResults.value = res.data.items
    // History list and voyage overview immediately reflect the new rows.
    await refreshList()
  } catch (e) {
    batchSubmitError.value = '无法连接 API：' + e.message
  } finally {
    batchSubmitting.value = false
  }
}

function addBatchRow() {
  if (batchRows.value.length >= MAX_BATCH_ROWS) return
  batchRows.value.push(blankRow())
}

function removeBatchRow(index) {
  if (batchRows.value.length === 1) {
    batchRows.value = [blankRow()]
    for (const k of Object.keys(batchErrors)) delete batchErrors[k]
    return
  }
  batchRows.value.splice(index, 1)
  // Re-map row-keyed errors so a highlight never sticks to the wrong row
  // after a line is deleted: errors on the removed row go away; later rows
  // shift down by one. Errors are fully recomputed on the next submit too.
  const remapped = {}
  for (const [k, v] of Object.entries(batchErrors)) {
    const [idxStr, ...rest] = k.split('.')
    const idx = Number(idxStr)
    if (idx === index) continue
    const next = idx > index ? idx - 1 : idx
    remapped[`${next}.${rest.join('.')}`] = v
  }
  for (const k of Object.keys(batchErrors)) delete batchErrors[k]
  Object.assign(batchErrors, remapped)
}

function switchMode(next) {
  mode.value = next
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
  <section class="grid" :class="{ 'grid-single-column': mode === 'batch' }">
    <div class="card form">
      <div class="mode-switch" role="tablist" aria-label="录入模式">
        <label class="mode-option" :class="{ active: mode === 'single' }">
          <input type="radio" name="entry-mode" value="single" :checked="mode === 'single'" @change="switchMode('single')" />
          单条录入
        </label>
        <label class="mode-option" :class="{ active: mode === 'batch' }">
          <input type="radio" name="entry-mode" value="batch" :checked="mode === 'batch'" @change="switchMode('batch')" />
          批量抄录（≤ 20 行）
        </label>
      </div>

      <!-- ===================== 单条录入 ===================== -->
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

      <!-- ===================== 批量抄录 ===================== -->
      <form v-else class="batch-form" novalidate @submit.prevent="submitBatch">
        <h2>靠港前批量抄录</h2>
        <p class="note">按<b>测量先后顺序</b>逐行填写航次、舱号、粮温、气温与湿度，一次提交。
          同航次同舱的后一行自动关联本批较早记录；不同舱位各自承接数据库中的最近前序。
          任一行不合法则整批不落库。</p>

        <div class="batch-table-wrap">
          <table class="batch-table">
            <thead>
              <tr>
                <th class="col-order">#</th>
                <th v-for="f in FIELDS" :key="f.key">{{ f.label }}</th>
                <th class="col-remove"></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(row, index) in batchRows"
                :key="index"
                :id="`batch-row-${index}`"
                class="batch-row"
                :class="{ 'row-invalid': batchProblemRows.includes(index) }"
              >
                <td class="col-order">{{ index + 1 }}</td>
                <td v-for="f in FIELDS" :key="f.key" class="batch-cell">
                  <input
                    :id="`bf-${index}-${f.key}`"
                    v-model="row[f.key]"
                    :type="f.type"
                    :min="f.min"
                    :max="f.max"
                    :step="f.step"
                    :placeholder="f.placeholder"
                    :aria-invalid="!!batchErrors[`${index}.${f.key}`]"
                    :aria-describedby="batchErrors[`${index}.${f.key}`] ? `berr-${index}-${f.key}` : null"
                  />
                  <p
                    v-if="batchErrFor(index, f.key)"
                    :id="`berr-${index}-${f.key}`"
                    class="field-err"
                  >{{ batchErrFor(index, f.key) }}</p>
                </td>
                <td class="col-remove">
                  <button
                    type="button"
                    class="btn-remove"
                    :disabled="batchSubmitting"
                    :aria-label="`删除第 ${index + 1} 行`"
                    :title="`删除第 ${index + 1} 行`"
                    @click="removeBatchRow(index)"
                  >×</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="batch-actions">
          <button
            type="button"
            class="btn-add"
            :disabled="batchSubmitting || batchRows.length >= MAX_BATCH_ROWS"
            @click="addBatchRow"
          >＋ 添加一行（{{ batchRows.length }}/{{ MAX_BATCH_ROWS }}）</button>
        </div>

        <p v-if="batchSubmitError" class="banner-error" data-test="batch-banner-error">{{ batchSubmitError }}</p>

        <button type="submit" class="btn-primary" :disabled="batchSubmitting">
          {{ batchSubmitting ? '批量复算中…' : `批量提交复算（${filledBatchRows.length} 行）` }}
        </button>
      </form>
    </div>

    <div class="side">
      <!-- 单条结果 -->
      <div v-if="mode === 'single'">
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
      </div>

      <!-- 批量结果 -->
      <div v-else>
        <div v-if="batchResults" class="card result" data-test="batch-result">
          <h2>本批评估结果（{{ batchResults.length }} 行）</h2>
          <p class="note">各行按提交顺序获得连续评估编号；点击编号进入既有公式明细与前序对照。</p>
          <table class="batch-result-table">
            <thead>
              <tr><th>行</th><th>评估编号</th><th>航次/舱号</th><th>Δ（℃）</th><th>结论</th></tr>
            </thead>
            <tbody>
              <tr v-for="(a, i) in batchResults" :key="a.id" data-test="batch-result-row">
                <td>{{ i + 1 }}</td>
                <td><RouterLink :to="`/assessments/${a.id}`" class="link">#{{ a.id }}</RouterLink></td>
                <td>{{ a.voyage }} / {{ a.hatch }}</td>
                <td class="strong">{{ fmt2(a.delta_display) }}</td>
                <td><VerdictBadge :verdict="a.verdict" :hint="false" /></td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="card placeholder">
          <h2>本批评估结果</h2>
          <p>批量提交成功后，此处逐行显示评估编号、未舍入温差与结论；历史列表与舱位概览会立即反映新记录。</p>
        </div>
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
