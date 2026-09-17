import { expect, test } from '@playwright/test'

// Every test goes through the real browser -> vite proxy -> Go Gin ->
// SQLite stack. The browser never computes a verdict itself.

async function fillForm(page, values) {
  for (const [key, value] of Object.entries(values)) {
    await page.fill(`#f-${key}`, String(value))
  }
}

const VALID = { voyage: 'V-E2E-1', hatch: '3H', tg: '25', ta: '20', rh: '70' }

test.beforeEach(async ({ page }) => {
  await page.goto('/')
})

test('valid submission shows API verdict, detail formula and survives reload', async ({ page }) => {
  await fillForm(page, VALID)
  await page.click('button[type=submit]')

  // Result card renders the API values, not a local recomputation.
  await expect(page.locator('.result')).toBeVisible()
  await expect(page.locator('.result .v-allowed')).toContainText('允许通风')
  await expect(page.locator('.result')).toContainText('14.36')
  await expect(page.locator('.result')).toContainText('10.64')

  // The history table shows the same verdict after a full page reload,
  // proving the conclusion was persisted and is read back from SQLite.
  await page.reload()
  const row = page.locator('tbody tr', { hasText: 'V-E2E-1' })
  await expect(row).toBeVisible()
  await expect(row.locator('.v-allowed')).toContainText('允许通风')
  await expect(row).toContainText('10.64')

  // Detail page lists each substituted formula line from the API.
  await row.locator('a').click()
  await expect(page).toHaveURL(/\/assessments\/\d+$/)
  await expect(page.locator('.formula')).toContainText('ln(70/100)')
  await expect(page.locator('.formula')).toContainText('17.62 × 20 / (243.12 + 20)')
  await expect(page.locator('.formula')).toContainText('Δ = Tg − Td = 25 −')
  await expect(page.locator('.detail .v-allowed')).toContainText('允许通风')
})

test('field-level errors for out-of-range values block submission', async ({ page }) => {
  await fillForm(page, { ...VALID, voyage: 'V-RANGE-REJECT', tg: '60.5', rh: '120' })
  await page.click('button[type=submit]')

  await expect(page.locator('#err-tg')).toContainText('60.0')
  await expect(page.locator('#err-rh')).toContainText('100.0')
  await expect(page.locator('.result')).toHaveCount(0)
  // Nothing was persisted.
  await page.reload()
  await expect(page.locator('tbody tr', { hasText: 'V-RANGE-REJECT' })).toHaveCount(0)
})

test('a server 422 payload is rendered as a field error with no record created', async ({ page }) => {
  // Client and server ranges are identical, so a valid form normally never
  // sees a 422. Simulate the server rejecting one field (e.g. a future rule
  // change) to prove the UI renders the API's field errors verbatim.
  await page.route('**/api/assessments', async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    await route.fulfill({
      status: 422,
      contentType: 'application/json',
      body: JSON.stringify({
        error: '输入校验失败，未生成任何记录',
        fields: [{ field: 'ta', code: 'out_of_range', message: '舱内气温 Ta必须在 -20.0 至 60.0 之间' }],
      }),
    })
  })

  await fillForm(page, { ...VALID, voyage: 'V-422' })
  await page.click('button[type=submit]')

  await expect(page.locator('#err-ta')).toContainText('60.0')
  await expect(page.locator('.banner-error')).toContainText('未生成任何记录')
  await expect(page.locator('.result')).toHaveCount(0)
})

// The crucial boundary discipline: inputs whose delta DISPLAYS as exactly
// ±2.00 but whose unrounded value is inside the closed band must retest,
// while 0.00x outside may decide strictly. All four verdicts here come from
// the API; the page only renders them.
test('displayed ±2.00 endpoints both retest; neighbours decide strictly', async ({ page }) => {
  const cases = [
    { tg: '16.357', delta: '2.00', cls: 'v-retest', label: '暂停并复测' },
    { tg: '12.36', delta: '-2.00', cls: 'v-retest', label: '暂停并复测' },
    { tg: '16.36', delta: '2.00', cls: 'v-allowed', label: '允许通风' },
    { tg: '12.357', delta: '-2.00', cls: 'v-denied', label: '禁止通风' },
  ]
  for (const c of cases) {
    await page.goto('/')
    await fillForm(page, { voyage: `V-B-${c.tg}`, hatch: 'H', tg: c.tg, ta: '20', rh: '70' })
    await page.click('button[type=submit]')
    await expect(page.locator('.result')).toBeVisible()
    await expect(page.locator('.result')).toContainText(c.delta)
    await expect(page.locator(`.result .${c.cls}`)).toContainText(c.label)
  }
})

test('denied case: cold grain with humid air', async ({ page }) => {
  await fillForm(page, { voyage: 'V-COLD', hatch: '1P', tg: '5', ta: '28', rh: '95' })
  await page.click('button[type=submit]')
  await expect(page.locator('.result .v-denied')).toContainText('禁止通风')
})

// Contract tests against the REAL Gin server (no UI, no mocks): non-finite
// or out-of-range numbers must answer 422 and must not create a row.
test.describe('real API rejects invalid submissions with 422', () => {
  const validBody = { voyage: 'V-API', hatch: 'H', tg: 25, ta: 20, rh: 70 }

  async function postInvalid(request, patch) {
    const res = await request.post('/api/assessments', {
      data: { ...validBody, ...patch },
      headers: { 'Content-Type': 'application/json' },
    })
    expect(res.status()).toBe(422)
    const body = await res.json()
    expect(Array.isArray(body.fields)).toBeTruthy()
    return body
  }

  test('out-of-range and non-finite values return field errors', async ({ request }) => {
    const r1 = await postInvalid(request, { tg: 60.01 })
    expect(r1.fields[0].field).toBe('tg')

    // Node's JSON.stringify(Infinity) emits "null", so send the raw
    // overflowing literal the way a misbehaving client would; Go's JSON
    // decoder rejects 1e999 and the API must report a non-finite field.
    const raw = await request.post('/api/assessments', {
      headers: { 'Content-Type': 'application/json' },
      data: '{"voyage":"V-API","hatch":"H","tg":1e999,"ta":20,"rh":70}',
    })
    expect(raw.status()).toBe(422)
    const rawBody = await raw.json()
    expect(rawBody.fields.some((f) => f.field === 'tg' && f.code === 'not_finite')).toBeTruthy()

    const r3 = await postInvalid(request, { tg: -21, ta: 99, rh: 0 })
    expect(r3.fields.map((f) => f.field).sort()).toEqual(['rh', 'ta', 'tg'])
  })

  test('a rejected submission cannot be read back from the list endpoint', async ({ request }) => {
    await postInvalid(request, { voyage: 'V-NEVER-SAVED', rh: 100.5 })
    const res = await request.get('/api/assessments')
    const body = await res.json()
    expect(body.items.some((a) => a.voyage === 'V-NEVER-SAVED')).toBeFalsy()
  })
})
