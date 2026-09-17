import { expect, test } from '@playwright/test'

// End-to-end coverage for predecessor comparison. Every value on the page,
// including the unrounded change quantities, is served by Go; these tests
// assert the reloaded DOM matches the server's JSON field-for-field.

async function fillForm(page, values) {
  for (const [key, value] of Object.entries(values)) {
    await page.fill(`#f-${key}`, String(value))
  }
}

async function submitAndOpenDetail(page, values) {
  await page.goto('/')
  await fillForm(page, values)
  await page.click('button[type=submit]')
  await expect(page.locator('.result')).toBeVisible()
  const href = await page.locator('.result a').getAttribute('href')
  await page.goto(href)
  await expect(page.locator('.detail')).toBeVisible()
  return Number(href.match(/\/assessments\/(\d+)/)[1])
}

const RUN = Date.now()

test('first measurement shows the first-measurement prompt', async ({ page }) => {
  const id = await submitAndOpenDetail(page, { voyage: `V-E2E-A-${RUN}`, hatch: 'H1', tg: '25', ta: '20', rh: '70' })

  const hint = page.locator('[data-test=first-measurement]')
  await expect(hint).toBeVisible()
  await expect(hint).toContainText('首次测量')
  await expect(page.locator('[data-test=compare-card]')).toHaveCount(0)

  // The API response for a first measurement carries no comparison block.
  const detail = await page.request.get(`/api/assessments/${id}`).then((r) => r.json())
  expect(detail.comparison).toBeUndefined()
})

test('repeat measurement gets a comparison card; interleaved hatch never crosses chains; values survive reload from server', async ({ page }) => {
  const voy = `V-E2E-B-${RUN}`
  // 1. First measurement of H1.
  const id1 = await submitAndOpenDetail(page, { voyage: voy, hatch: 'H1', tg: '25', ta: '20', rh: '70' })
  await expect(page.locator('[data-test=first-measurement]')).toBeVisible()

  // 2. A DIFFERENT hatch submitted in between must not steal the chain.
  const idOther = await submitAndOpenDetail(page, { voyage: voy, hatch: 'H2', tg: '24', ta: '20', rh: '70' })
  await expect(page.locator('[data-test=first-measurement]')).toBeVisible()

  // 3. Second measurement of H1 chains to id1, NOT to the interleaved H2 row.
  const id2 = await submitAndOpenDetail(page, { voyage: voy, hatch: 'H1', tg: '23.5', ta: '21', rh: '75' })

  const apiBefore = await page.request.get(`/api/assessments/${id2}`).then((r) => r.json())
  expect(apiBefore.comparison.available).toBe(true)
  expect(apiBefore.comparison.previous.id).toBe(id1)
  expect(apiBefore.comparison.previous.id).not.toBe(idOther)
  expect(apiBefore.comparison.previous.hatch).toBe('H1')

  const card = page.locator('[data-test=compare-card]')
  await expect(card).toBeVisible()
  await expect(card.locator('a')).toHaveAttribute('href', `/assessments/${id1}`)
  await expect(card).toContainText('粮温 Tg 变化')
  await expect(card).toContainText('舱内气温 Ta 变化')
  await expect(card).toContainText('相对湿度 RH 变化')
  await expect(card).toContainText('露点 Td 变化（未舍入）')
  await expect(card).toContainText('温差 Δ 变化（未舍入）')

  // Full reload: the card must still show exactly what the server computes.
  await page.reload()
  await expect(page.locator('.detail')).toBeVisible()
  const api = await page.request.get(`/api/assessments/${id2}`).then((r) => r.json())
  expect(api.comparison.available).toBe(true)

  const rows = ['tg', 'ta', 'rh', 'td', 'delta']
  const text = await page.locator('[data-test=compare-card]').textContent()
  for (const key of rows) {
    const expected = api.comparison.changes[key]
    const signed = expected > 0 ? `+${expected}` : String(expected)
    expect(text).toContain(signed)
  }
  // Unrounded persisted values of the predecessor also come from the server.
  expect(text).toContain(String(api.comparison.previous.td))
  expect(text).toContain(String(api.comparison.previous.delta))

  // Predecessor detail remains traceable: its own page is a first/earlier record.
  await page.goto(`/assessments/${id1}`)
  await expect(page.locator('[data-test=first-measurement]')).toBeVisible()
})

test('comparison block only exists on detail responses, never on POST or list', async ({ request }) => {
  const v = `V-E2E-CMP-${Date.now()}`
  const first = await request.post('/api/assessments', {
    headers: { 'Content-Type': 'application/json' },
    data: { voyage: v, hatch: 'H', tg: 25, ta: 20, rh: 70 },
  }).then((r) => r.json())
  const second = await request.post('/api/assessments', {
    headers: { 'Content-Type': 'application/json' },
    data: { voyage: v, hatch: 'H', tg: 24, ta: 20, rh: 70 },
  }).then((r) => r.json())

  expect(first.comparison).toBeUndefined()
  expect(second.comparison).toBeUndefined()

  const list = await request.get('/api/assessments').then((r) => r.json())
  for (const item of list.items) {
    expect(item.comparison).toBeUndefined()
  }

  const detail = await request.get(`/api/assessments/${second.id}`).then((r) => r.json())
  expect(detail.comparison.available).toBe(true)
  expect(detail.comparison.previous.id).toBe(first.id)
})
