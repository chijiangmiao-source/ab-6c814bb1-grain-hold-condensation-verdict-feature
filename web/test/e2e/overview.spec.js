import { expect, test } from '@playwright/test'

// End-to-end acceptance for the voyage "hatch overview":
//   1. interleave submissions of MULTIPLE hatches across TWO voyages,
//      measuring one hatch repeatedly;
//   2. enter the overview for the TARGET voyage from the history area;
//   3. confirm only that voyage's LATEST record per hatch is shown and a row
//      jumps to the matching detail;
//   4. reload and confirm the selection is unchanged (decided server-side).
//
// Latestness is decided by the server with MAX(id) per hatch; the browser
// only renders the returned snapshots.

const RUN = Date.now()
const V1 = `V-OV-A-${RUN}`
const V2 = `V-OV-B-${RUN}`

async function submit(request, voyage, hatch, tg) {
  const res = await request.post('/api/assessments', {
    headers: { 'Content-Type': 'application/json' },
    data: { voyage, hatch, tg, ta: 20, rh: 70 },
  })
  expect(res.status(), await res.text()).toBe(201)
  return res.json()
}

test.beforeEach(async ({ request }) => {
  // Interleaved across two voyages; hatch 2P of V1 is measured three times.
  await submit(request, V1, '2P', 24) // a1
  await submit(request, V2, '2P', 5) // other voyage, same hatch number
  await submit(request, V1, '3H', 25)
  await submit(request, V1, '2P', 23) // a2, repeat (not the latest)
  await submit(request, V2, '3H', 6)
  await submit(request, V1, '2P', 26) // a3, latest 2P
  await submit(request, V1, '4H', 16.357) // Δ displays 2.00 but unrounded < 2 -> retest
})

test('overview lists only the target voyage latest record per hatch, reachable from history', async ({ page, request }) => {
  // --- API contract: server groups to MAX(id) per hatch, sorted by hatch ---
  const api = await request.get(`/api/voyages/${encodeURIComponent(V1)}/hatches/latest`).then((r) => r.json())
  expect(api.voyage).toBe(V1)
  expect(api.items).toHaveLength(3)
  expect(api.items.map((i) => i.hatch)).toEqual(['2P', '3H', '4H'])

  const all = await request.get('/api/assessments').then((r) => r.json())
  const v1Rows = all.items.filter((i) => i.voyage === V1)
  const v2Rows = all.items.filter((i) => i.voyage === V2)
  const latestIdByHatch = {}
  for (const h of ['2P', '3H', '4H']) {
    latestIdByHatch[h] = v1Rows.filter((i) => i.hatch === h)[0].id // list is newest first
  }
  expect(api.items.map((i) => i.id)).toEqual(
    ['2P', '3H', '4H'].map((h) => latestIdByHatch[h]))

  // The repeat hatch 2P resolves to the THIRD measurement, not the first two.
  const hatch2P = v1Rows.filter((i) => i.hatch === '2P')
  expect(hatch2P).toHaveLength(3)
  expect(latestIdByHatch['2P']).toBe(hatch2P[0].id)
  expect(latestIdByHatch['2P']).not.toBe(hatch2P[2].id)

  // Snapshot values are the persisted latest row's own values/verdict.
  const byHatch = Object.fromEntries(api.items.map((i) => [i.hatch, i]))
  expect(byHatch['2P'].verdict).toBe('allowed')
  expect(byHatch['4H'].verdict).toBe('retest')
  expect(byHatch['4H'].delta_display).toBe(2.0)
  for (const row of api.items) {
    expect(row.voyage).toBe(V1)
    expect(row.comparison).toBeUndefined()
    expect(row.formula).toBeUndefined()
  }

  // The other voyage is independently grouped and never leaks into V1.
  const api2 = await request.get(`/api/voyages/${encodeURIComponent(V2)}/hatches/latest`).then((r) => r.json())
  expect(api2.items).toHaveLength(2)
  expect(api2.items.every((i) => i.voyage === V2)).toBe(true)
  expect(v2Rows).toHaveLength(2)

  // --- UI: enter from the history area ---
  await page.goto('/')
  const entry = page.locator('[data-test=voyage-entry]', { hasText: V1 })
  await expect(entry).toHaveAttribute('href', `/voyages/${encodeURIComponent(V1)}/hatches/latest`)
  await entry.click()
  await expect(page).toHaveURL(/\/voyages\/.*\/hatches\/latest$/)
  await expect(page.locator('[data-test=overview]')).toBeVisible()

  const rows = page.locator('[data-test=overview-row]')
  await expect(rows).toHaveCount(3)
  expect(await rows.evaluateAll((trs) => trs.map((tr) => tr.cells[0].textContent.trim())))
    .toEqual(['2P', '3H', '4H'])
  expect(await rows.evaluateAll((trs) => trs.map((tr) => tr.cells[1].textContent.trim())))
    .toEqual(['2P', '3H', '4H'].map((h) => `#${latestIdByHatch[h]}`))

  // No row belongs to the other voyage.
  const overviewText = await page.locator('[data-test=overview]').textContent()
  expect(overviewText).not.toContain(V2)
  // The boundary hatch shows the SERVER verdict, not one derived from 2.00.
  const row4H = rows.nth(2)
  await expect(row4H).toContainText('暂停并复测')
  await expect(row4H).toContainText('2.00')

  // --- A row jumps to the matching detail (the latest 2P record) ---
  const latest2PDetail = `/assessments/${latestIdByHatch['2P']}`
  await expect(rows.nth(0).locator('a')).toHaveAttribute('href', latest2PDetail)
  await rows.nth(0).locator('a').click()
  await expect(page).toHaveURL(new RegExp(`${latest2PDetail}$`))
  await expect(page.locator('.detail')).toBeVisible()
  await expect(page.locator('.detail h2')).toContainText(`#${latestIdByHatch['2P']}`)
  // It is the repeat measurement, so its predecessor comparison is available.
  await expect(page.locator('[data-test=compare-card]')).toBeVisible()

  // --- Back to the overview; reload keeps the same server selection ---
  await page.goto(`/voyages/${encodeURIComponent(V1)}/hatches/latest`)
  await expect(rows).toHaveCount(3)
  await page.reload()
  await expect(page.locator('[data-test=overview-row]')).toHaveCount(3)
  expect(await page.locator('[data-test=overview-row]').evaluateAll(
    (trs) => trs.map((tr) => tr.cells[1].textContent.trim())))
    .toEqual(['2P', '3H', '4H'].map((h) => `#${latestIdByHatch[h]}`))

  // The way back to the history area is always present (above the card).
  await page.getByRole('link', { name: /返回历史区/ }).first().click()
  await expect(page).toHaveURL(/\/$/)
})

test('an unknown voyage shows an empty overview that keeps the history entry', async ({ page }) => {
  const missing = `V-OV-MISSING-${RUN}`
  await page.goto(`/voyages/${encodeURIComponent(missing)}/hatches/latest`)
  await expect(page.locator('[data-test=overview-empty]')).toBeVisible()
  await expect(page.locator('[data-test=overview-row]')).toHaveCount(0)

  const back = page.locator('a', { hasText: '返回历史区' }).first()
  await back.click()
  await expect(page).toHaveURL(/\/$/)
})
