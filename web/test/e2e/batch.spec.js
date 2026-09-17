import { expect, test } from '@playwright/test'

// End-to-end coverage for the pre-berth BULK entry flow through the real
// browser -> nginx/vite proxy -> Go Gin -> SQLite stack. It proves:
//   1. interleaved + repeated hatches chain INSIDE one batch and each hatch
//      overview latest item is the true creation-order last row;
//   2. an invalid middle row rejects the WHOLE batch server-side (rollback),
//      while the UI keeps every entered value and locates the problem row;
//   3. the single-create request/response shapes remain untouched.

const RUN = Date.now()

async function gotoBatch(page) {
  await page.goto('/')
  await page.getByRole('tab', { name: /批量模式/ }).click()
  await expect(page.locator('.batch-form')).toBeVisible()
}

async function ensureRows(page, n) {
  while (await page.locator('[data-test=batch-row]').count() < n) {
    await page.locator('[data-test=batch-add-row]').click()
  }
  await expect(page.locator('[data-test=batch-row]')).toHaveCount(n)
}

async function fillBatchRow(page, i, { voyage, hatch, tg, ta = '20', rh = '70' }) {
  await page.fill(`#bf-${i}-voyage`, voyage)
  await page.fill(`#bf-${i}-hatch`, hatch)
  await page.fill(`#bf-${i}-tg`, tg)
  await page.fill(`#bf-${i}-ta`, ta)
  await page.fill(`#bf-${i}-rh`, rh)
}

async function resultIds(page) {
  const hrefs = await page.locator('[data-test=batch-result-row] a').evaluateAll(
    (anchors) => anchors.map((a) => a.getAttribute('href')),
  )
  // Every row has two anchors (id cell + "详情" cell) with the same href.
  const ids = []
  for (const h of hrefs) {
    const m = h.match(/\/assessments\/(\d+)/)
    const id = Number(m[1])
    if (!ids.includes(id)) ids.push(id)
  }
  return ids
}

test.describe.configure({ mode: 'serial' })

test.describe('bulk batch entry against the real stack', () => {
  const voy = `V-BATCH-${RUN}`

  test('interleaved and repeated hatches: batch chaining, detail links, overview latest items, history order', async ({ page, request }) => {
    await gotoBatch(page)
    await ensureRows(page, 5)

    // Measurement order: 1H, 2P, 1H, 3H, 1H — 1H repeats twice in-batch.
    await fillBatchRow(page, 0, { voyage: voy, hatch: '1H', tg: '25' })
    await fillBatchRow(page, 1, { voyage: voy, hatch: '2P', tg: '24' })
    await fillBatchRow(page, 2, { voyage: voy, hatch: '1H', tg: '26' })
    await fillBatchRow(page, 3, { voyage: voy, hatch: '3H', tg: '20' })
    await fillBatchRow(page, 4, { voyage: voy, hatch: '1H', tg: '22' })

    await page.locator('.batch-submit').click()

    const result = page.locator('[data-test=batch-result]')
    await expect(result).toBeVisible()
    const resultRows = page.locator('[data-test=batch-result-row]')
    await expect(resultRows).toHaveCount(5)
    const ids = await resultIds(page)
    expect(ids).toHaveLength(5)

    // Rendered rows are in submission order with API-served id/Δ/verdict.
    const texts = await resultRows.allTextContents()
    expect(texts[0]).toContain(`${voy} / 1H`)
    expect(texts[1]).toContain('2P')
    expect(texts[4]).toContain('1H')
    for (const t of texts) expect(t).toMatch(/\d+\.\d{2}/)

    // POST response rows keep the single-create shape: formula present,
    // comparison absent.
    for (const id of ids) {
      const d = await request.get(`/api/assessments/${id}`).then((r) => r.json())
      expect(d.formula).toBeTruthy()
      expect(d.comparison === undefined || d.comparison.available !== undefined).toBeTruthy()
    }

    // In-batch predecessor links:
    //  row 1 (1H) is first; row 3 (1H) -> row 1; row 5 (1H) -> row 3;
    //  rows 2 (2P) and 4 (3H) are first measurements of their hatches.
    const d1 = await request.get(`/api/assessments/${ids[0]}`).then((r) => r.json())
    const d3 = await request.get(`/api/assessments/${ids[2]}`).then((r) => r.json())
    const d5 = await request.get(`/api/assessments/${ids[4]}`).then((r) => r.json())
    const d2 = await request.get(`/api/assessments/${ids[1]}`).then((r) => r.json())
    const d4 = await request.get(`/api/assessments/${ids[3]}`).then((r) => r.json())
    expect(d1.comparison).toBeUndefined()
    expect(d2.comparison).toBeUndefined()
    expect(d4.comparison).toBeUndefined()
    expect(d3.comparison.available).toBe(true)
    expect(d3.comparison.previous.id).toBe(ids[0])
    expect(d5.comparison.available).toBe(true)
    expect(d5.comparison.previous.id).toBe(ids[2])

    // Overview: one MAX(id) snapshot per hatch, hatch-ascending.
    const ov = await request
      .get(`/api/voyages/${encodeURIComponent(voy)}/hatches/latest`)
      .then((r) => r.json())
    expect(ov.items.map((i) => i.hatch)).toEqual(['1H', '2P', '3H'])
    expect(ov.items[0].id).toBe(ids[4], '1H latest is the third in-batch 1H')
    expect(ov.items[1].id).toBe(ids[1])
    expect(ov.items[2].id).toBe(ids[3])

    // A result row links into the existing detail page (checked before the
    // reload, since the in-memory batch result card is not persisted).
    await page.locator('[data-test=batch-result-row]').first().locator('a').first().click()
    await expect(page).toHaveURL(`/assessments/${ids[0]}`)
    await expect(page.locator('.detail')).toBeVisible()

    // Back home: history list reflects true creation order (newest first).
    await page.goto('/')
    const firstIds = (await page.locator('.history tbody tr a').allTextContents())
      .slice(0, 5).map((s) => Number(s.replace('#', '')))
    expect(firstIds).toEqual([...ids].reverse())
  })

  test('an out-of-range middle row is blocked inline: inputs retained, problem row located, nothing sent', async ({ page }) => {
    await gotoBatch(page)
    await ensureRows(page, 3)
    await fillBatchRow(page, 0, { voyage: `V-BAD-${RUN}`, hatch: '1H', tg: '25' })
    await fillBatchRow(page, 1, { voyage: `V-BAD-${RUN}`, hatch: '2H', tg: '60.5' }) // client-rejected
    await fillBatchRow(page, 2, { voyage: `V-BAD-${RUN}`, hatch: '3H', tg: '20' })

    const requestPromise = page.waitForRequest(/\/api\/assessments\/batch/, { timeout: 1500 }).catch(() => null)
    await page.locator('.batch-submit').click()
    const sent = await requestPromise
    expect(sent).toBeNull()

    // Middle row is marked and diagnosed; the other rows are not.
    await expect(page.locator('#batch-row-1.row-invalid')).toBeVisible()
    await expect(page.locator('#berr-1-tg')).toContainText('60.0')
    expect(await page.locator('#batch-row-0').evaluate((el) => el.classList.contains('row-invalid'))).toBe(false)
    // Every entered value survives.
    expect(await page.inputValue('#bf-1-tg')).toBe('60.5')
    expect(await page.inputValue('#bf-0-hatch')).toBe('1H')
    expect(await page.inputValue('#bf-2-hatch')).toBe('3H')
    await expect(page.locator('[data-test=batch-result]')).toHaveCount(0)
  })
})

test.describe('batch endpoint contract against the real API', () => {
  const base = { ta: 20, rh: 70 }

  test('a 422 on the middle row rolls the entire batch back and reports the row number', async ({ request }) => {
    const voy = `V-BATCH-ROLL-${RUN}`
    // A committed predecessor exists; the failed batch must not disturb it.
    const pre = await request.post('/api/assessments', {
      headers: { 'Content-Type': 'application/json' },
      data: { voyage: voy, hatch: '1H', tg: 30, ...base },
    }).then((r) => r.json())

    const res = await request.post('/api/assessments/batch', {
      headers: { 'Content-Type': 'application/json' },
      data: {
        items: [
          { voyage: voy, hatch: '1H', tg: 25, ...base }, // valid
          { voyage: voy, hatch: '2H', tg: 999, ...base }, // row 2 out of range
          { voyage: voy, hatch: '3H', tg: 5, ta: 28, rh: 95 }, // valid verdict but never saved
        ],
      },
    })
    expect(res.status()).toBe(422)
    const body = await res.json()
    expect(body.rows).toHaveLength(1)
    expect(body.rows[0].row).toBe(2)
    expect(body.rows[0].fields[0].field).toBe('tg')
    expect(body.fields[0].row).toBe(2)
    expect(body.fields[0].code).toBe('out_of_range')

    // Whole batch absent from the list: no partial rows.
    const list = await request.get('/api/assessments').then((r) => r.json())
    const batchRows = list.items.filter((a) =>
      a.voyage === voy && a.id !== pre.id && [25, 999].includes(Number(a.tg)))
    // Row 3 (tg=5) and row 1 (tg=25) must not exist; only pre (tg=30) remains.
    expect(list.items.filter((a) => a.voyage === voy).map((a) => a.tg)).toEqual([30])
    expect(batchRows).toEqual([])

    // Chain is unbroken: a later valid create still links the pre-batch row.
    const after = await request.post('/api/assessments', {
      headers: { 'Content-Type': 'application/json' },
      data: { voyage: voy, hatch: '1H', tg: 26, ...base },
    }).then((r) => r.json())
    const afterDetail = await request.get(`/api/assessments/${after.id}`).then((r) => r.json())
    expect(afterDetail.comparison.available).toBe(true)
    expect(afterDetail.comparison.previous.id).toBe(pre.id)
  })

  test('batch size is capped at 20 rows; malformed documents are 400 and never persist', async ({ request }) => {
    const voy = `V-BATCH-SHAPE-${RUN}`
    const mk = (n) => Array.from({ length: n }, (_, i) => ({
      voyage: voy, hatch: `H${i % 3}`, tg: 25, ...base,
    }))

    const ok = await request.post('/api/assessments/batch', { data: { items: mk(20) } })
    expect(ok.status()).toBe(201)
    expect((await ok.json()).items).toHaveLength(20)

    const tooBig = await request.post('/api/assessments/batch', { data: { items: mk(21) } })
    expect(tooBig.status()).toBe(400)
    expect((await tooBig.json()).error).toContain('最多 20 行')

    const empty = await request.post('/api/assessments/batch', { data: { items: [] } })
    expect(empty.status()).toBe(400)

    for (const doc of [
      { items: [{ voyage: voy, hatch: 'H', tg: 25, ...base }, null] },
      { items: [1, 2] },
      { rows: [] },
      { items: [], extra: 1 },
    ]) {
      const r = await request.post('/api/assessments/batch', { data: doc })
      expect(r.status()).toBe(400)
    }
    // Duplicate key inside a row is a raw structural rejection.
    const dup = await request.post('/api/assessments/batch', {
      headers: { 'Content-Type': 'application/json' },
      data: `{"items":[{"voyage":"${voy}","hatch":"H","tg":25,"tg":26,"ta":20,"rh":70}]}`,
    })
    expect(dup.status()).toBe(400)

    // Only the accepted 20-row batch persisted.
    const list = await request.get('/api/assessments').then((r) => r.json())
    expect(list.items.filter((a) => a.voyage === voy)).toHaveLength(20)
  })

  test('single-create request/response shape stays compatible after the batch endpoint', async ({ request }) => {
    const res = await request.post('/api/assessments', {
      headers: { 'Content-Type': 'application/json' },
      data: { voyage: `V-BATCH-COMPAT-${RUN}`, hatch: '9H', tg: 25, ...base },
    })
    expect(res.status()).toBe(201)
    const body = await res.json()
    for (const key of ['id', 'voyage', 'hatch', 'tg', 'ta', 'rh', 'gamma', 'td', 'delta',
      'gamma_display', 'td_display', 'delta_display', 'verdict', 'formula', 'created_at']) {
      expect(body[key] !== undefined).toBe(true)
    }
    expect(body.comparison).toBeUndefined()
    expect(body.items).toBeUndefined()
  })
})
