import { expect, test } from '@playwright/test'

// End-to-end acceptance for pre-berthing BATCH transcription:
//   1. switch the entry page to batch mode and submit an interleaved batch
//      (several hatches, some hatches measured repeatedly);
//   2. the result table shows each row's assessment id, Δ and verdict, each
//      id opens the existing detail and in-batch repeats chain to the earlier
//      in-batch row; history and the hatch overview reflect creation order;
//   3. an out-of-range MIDDLE row rejects the whole batch (422): every input
//      is retained, the problem row is marked and located, nothing persists;
//   4. fixing the row resubmits the whole batch successfully.
//
// All numbers/verdicts come from the real Go API; the browser only renders.

const RUN = Date.now()

async function gotoBatch(page) {
  await page.goto('/')
  // The radio input is visually hidden behind its styled label; click the
  // label the way a user does.
  await page.locator('.mode-option', { hasText: '批量抄录' }).click()
  await expect(page.locator('.batch-form')).toBeVisible()
}

async function fillBatchRows(page, rows) {
  for (let i = 0; i < rows.length; i++) {
    for (const [key, value] of Object.entries(rows[i])) {
      await page.fill(`#bf-${i}-${key}`, String(value))
    }
  }
}

const FIELDS = { ta: 20, rh: 70 }

test('interleaved/repeated-hatch batch: ordered ids, chains, overview latest, detail links', async ({ page, request }) => {
  const voy = `V-BATCH-A-${RUN}`
  const rows = [
    { voyage: voy, hatch: '1P', tg: '25', ...FIELDS }, // a1 -> first measurement
    { voyage: voy, hatch: '2S', tg: '5', ta: '28', rh: '95' }, // b1 denied, first
    { voyage: voy, hatch: '1P', tg: '24', ...FIELDS }, // a2 -> chains to a1
    { voyage: voy, hatch: '3H', tg: '16.357', ...FIELDS }, // c1 retest, first
    { voyage: voy, hatch: '2S', tg: '26', ...FIELDS }, // b2 -> chains to b1
  ]

  await gotoBatch(page)
  await fillBatchRows(page, rows)
  await page.click('.batch-form button[type=submit]')

  const result = page.locator('[data-test=batch-result]')
  await expect(result).toBeVisible()
  const resultRows = page.locator('[data-test=batch-result-row]')
  await expect(resultRows).toHaveCount(5)

  // Ids ascend strictly in measurement order; verdicts are the server's.
  const ids = []
  for (let i = 0; i < 5; i++) {
    const idText = (await resultRows.nth(i).locator('a').textContent()).trim()
    ids.push(Number(idText.replace('#', '')))
    if (i > 0) expect(ids[i]).toBeGreaterThan(ids[i - 1])
  }
  await expect(resultRows.nth(0)).toContainText('允许通风')
  await expect(resultRows.nth(1)).toContainText('禁止通风')
  await expect(resultRows.nth(3)).toContainText('暂停并复测')

  // Each id links to the existing detail route.
  await expect(resultRows.nth(2).locator('a')).toHaveAttribute('href', `/assessments/${ids[2]}`)

  // The repeat 1P row (3rd, a2) chains to the EARLIER IN-BATCH row a1, and
  // 2S's repeat (b2) chains to b1 — not to global recency.
  for (const [curIdx, prevIdx] of [[2, 0], [4, 1]]) {
    const d = await request.get(`/api/assessments/${ids[curIdx]}`).then((r) => r.json())
    expect(d.comparison.available).toBe(true)
    expect(d.comparison.previous.id).toBe(ids[prevIdx])
  }
  // First measurements of the batch hatches have no comparison.
  for (const idx of [0, 1, 3]) {
    const d = await request.get(`/api/assessments/${ids[idx]}`).then((r) => r.json())
    expect(d.comparison).toBeUndefined()
  }

  // Open one detail straight from the result table.
  await resultRows.nth(2).locator('a').click()
  await expect(page).toHaveURL(`/assessments/${ids[2]}`)
  await expect(page.locator('[data-test=compare-card]')).toBeVisible()
  await expect(page.locator('[data-test=compare-card] a')).toHaveAttribute('href', `/assessments/${ids[0]}`)

  // History (newest first) immediately shows the batch rows in true order.
  await page.goto('/')
  const historyText = await page.locator('.history tbody').textContent()
  for (const id of ids) expect(historyText).toContain(String(id))

  // The hatch overview shows exactly the LAST batch row per hatch (MAX id).
  const ov = await request.get(`/api/voyages/${encodeURIComponent(voy)}/hatches/latest`).then((r) => r.json())
  expect(ov.items.map((i) => i.hatch)).toEqual(['1P', '2S', '3H'])
  const latest = Object.fromEntries(ov.items.map((i) => [i.hatch, i.id]))
  expect(latest['1P']).toBe(ids[2])
  expect(latest['2S']).toBe(ids[4])
  expect(latest['3H']).toBe(ids[3])
})

test('a middle out-of-range row is blocked, keeps inputs, and resubmits after fixing', async ({ page, request }) => {
  const voy = `V-BATCH-B-${RUN}`
  const rows = [
    { voyage: voy, hatch: '1P', tg: '25', ...FIELDS },
    { voyage: voy, hatch: '2S', tg: '999', ...FIELDS }, // row 2 illegal
    { voyage: voy, hatch: '1P', tg: '26', ...FIELDS },
  ]

  await gotoBatch(page)
  await fillBatchRows(page, rows)
  await page.click('.batch-form button[type=submit]')

  // The client range check (identical to decision.Validate) blocks the batch
  // locally: banner + row-local error; row 2 (0-based 1) is the only bad row.
  await expect(page.locator('[data-test=batch-banner-error]')).toBeVisible()
  await expect(page.locator('[data-test=batch-banner-error]')).toContainText('整批未提交')
  await expect(page.locator('#batch-row-1')).toHaveClass(/row-invalid/)
  await expect(page.locator('#berr-1-tg')).toContainText('60.0')
  await expect(page.locator('#batch-row-0')).not.toHaveClass(/row-invalid/)
  await expect(page.locator('[data-test=batch-result]')).toHaveCount(0)

  // Every typed value is retained.
  expect(await page.inputValue('#bf-1-tg')).toBe('999')
  expect(await page.inputValue('#bf-0-hatch')).toBe('1P')
  expect(await page.inputValue('#bf-2-hatch')).toBe('1P')

  // Nothing from this batch exists in the database.
  const list = await request.get('/api/assessments').then((r) => r.json())
  expect(list.items.some((a) => a.voyage === voy)).toBe(false)

  // Fix the middle row and resubmit the same three rows.
  await page.fill('#bf-1-tg', '24')
  await page.click('.batch-form button[type=submit]')
  await expect(page.locator('[data-test=batch-result]')).toBeVisible()
  const resultRows = page.locator('[data-test=batch-result-row]')
  await expect(resultRows).toHaveCount(3)
  const ids = []
  for (let i = 0; i < 3; i++) {
    ids.push(Number((await resultRows.nth(i).locator('a').textContent()).replace('#', '')))
  }

  // After the clean batch the third row (repeat 1P) chains to the first.
  const d = await request.get(`/api/assessments/${ids[2]}`).then((r) => r.json())
  expect(d.comparison.available).toBe(true)
  expect(d.comparison.previous.id).toBe(ids[0])
  const middle = await request.get(`/api/assessments/${ids[1]}`).then((r) => r.json())
  expect(middle.comparison).toBeUndefined()
})

// A genuine server-side batch 422 (simulated, since client and server ranges
// are identical) renders row-numbered field errors in place while keeping all
// inputs, and produces no result card.
test('a server batch 422 is mapped to the named row and keeps every input', async ({ page }) => {
  await page.route('**/api/assessments/batch', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    await route.fulfill({
      status: 422,
      contentType: 'application/json',
      body: JSON.stringify({
        error: '批量输入校验失败，未生成任何记录',
        fields: [{ row: 2, field: 'rh', code: 'out_of_range', message: '相对湿度 RH必须在 1.0 至 100.0 之间' }],
      }),
    })
  })

  await gotoBatch(page)
  await fillBatchRows(page, [
    { voyage: 'V-UI422', hatch: '1P', tg: '25', ta: '20', rh: '70' },
    { voyage: 'V-UI422', hatch: '2S', tg: '24', ta: '20', rh: '70' },
  ])
  await page.click('.batch-form button[type=submit]')

  await expect(page.locator('[data-test=batch-banner-error]')).toContainText('整批未保存')
  await expect(page.locator('#batch-row-1')).toHaveClass(/row-invalid/)
  await expect(page.locator('#berr-1-rh')).toContainText('100.0')
  await expect(page.locator('[data-test=batch-result]')).toHaveCount(0)
  // All inputs are preserved for in-place correction.
  expect(await page.inputValue('#bf-0-hatch')).toBe('1P')
  expect(await page.inputValue('#bf-1-hatch')).toBe('2S')
  expect(await page.inputValue('#bf-1-tg')).toBe('24')
})

test.describe('real batch API contract', () => {
  function row(i) {
    return { voyage: `V-BATCH-C-${RUN}`, hatch: `H${i}`, tg: 25, ta: 20, rh: 70 }
  }

  test('over-20 and empty batches are 400 and persist nothing', async ({ request }) => {
    const oversized = { measurements: Array.from({ length: 21 }, (_, i) => row(i)) }
    const r1 = await request.post('/api/assessments/batch', { data: oversized })
    expect(r1.status()).toBe(400)
    expect((await r1.json()).error).toContain('20')

    const r2 = await request.post('/api/assessments/batch', { data: { measurements: [] } })
    expect(r2.status()).toBe(400)

    const r3 = await request.post('/api/assessments/batch', { data: {} })
    expect(r3.status()).toBe(400)

    const list = await request.get('/api/assessments').then((r) => r.json())
    expect(list.items.some((a) => a.voyage === oversized.measurements[0].voyage)).toBe(false)
  })

  test('exactly twenty rows succeed with single-row DTO shapes', async ({ request }) => {
    const voyage = `V-BATCH-D-${RUN}`
    const measurements = Array.from({ length: 20 }, (_, i) => ({
      voyage, hatch: `H${String(i).padStart(2, '0')}`, tg: 25, ta: 20, rh: 70,
    }))
    const res = await request.post('/api/assessments/batch', { data: { measurements } })
    expect(res.status(), await res.text()).toBe(201)
    const body = await res.json()
    expect(body.items).toHaveLength(20)
    for (const item of body.items) {
      expect(item.formula).toBeTruthy()
      expect(item.comparison).toBeUndefined()
      expect(item.voyage).toBe(voyage)
    }
  })

  test('batch route coexists with single POST and detail GET', async ({ request }) => {
    // GET on the batch path must not be treated as assessment id "batch".
    const g = await request.get('/api/assessments/batch')
    expect(g.status()).not.toBe(200)

    const single = await request.post('/api/assessments', {
      headers: { 'Content-Type': 'application/json' },
      data: { voyage: `V-BATCH-E-${RUN}`, hatch: '1H', tg: 25, ta: 20, rh: 70 },
    })
    expect(single.status()).toBe(201)
    const s = await single.json()
    expect(s.formula).toBeTruthy()
    expect(s.comparison).toBeUndefined()

    const detail = await request.get(`/api/assessments/${s.id}`).then((r) => r.json())
    expect(detail.id).toBe(s.id)
    expect(detail.comparison).toBeUndefined()
  })
})
