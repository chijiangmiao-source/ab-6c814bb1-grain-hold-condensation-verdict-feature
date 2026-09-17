#!/usr/bin/env node
// Independent acceptance probe against the REAL running stack
// (http://api:8080). It recomputes the Magnus formula itself, then asserts
// the service returns the same numbers and the correct verdict. A fake or
// fixed-result API cannot pass these checks.

const BASE = process.env.ASSERT_API_ORIGIN || 'http://api:8080'
const A = 17.62
const B = 243.12

function expect(cond, message) {
  if (!cond) {
    console.error(`✗ ${message}`)
    process.exitCode = 1
    throw new Error(message)
  }
  console.log(`✓ ${message}`)
}

function round2(v) {
  return Math.round(v * 100) / 100
}

async function post(body) {
  const res = await fetch(`${BASE}/api/assessments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: typeof body === 'string' ? body : JSON.stringify(body),
  })
  const text = await res.text()
  return { status: res.status, body: text ? JSON.parse(text) : null }
}

async function main() {
  // 1. Health.
  const h = await fetch(`${BASE}/api/healthz`).then((r) => r.json())
  expect(h.status === 'ok', 'healthz reports ok')

  // 2. Known sample, independent recomputation.
  const sample = { voyage: 'ACCEPT-1', hatch: '3H', tg: 25, ta: 20, rh: 70 }
  const { status, body } = await post(sample)
  expect(status === 201, `valid sample is 201 (got ${status})`)

  const gamma = Math.log(sample.rh / 100) + (A * sample.ta) / (B + sample.ta)
  const td = (B * gamma) / (A - gamma)
  const delta = sample.tg - td
  expect(Math.abs(body.gamma - gamma) < 1e-12, `γ matches: api=${body.gamma} expected=${gamma}`)
  expect(Math.abs(body.td - td) < 1e-12, `Td matches: api=${body.td} expected=${td}`)
  expect(Math.abs(body.delta - delta) < 1e-12, `unrounded Δ matches: ${delta}`)
  expect(body.td_display === round2(td), `Td display rounds to 2 dp: ${body.td_display}`)
  expect(body.delta_display === round2(delta), `Δ display rounds to 2 dp: ${body.delta_display}`)
  expect(body.verdict === 'allowed', 'Δ ≈ 10.64 -> allowed')

  // 3. Reload consistency: GET detail and GET list must return the same
  // verdict and unrounded values as the POST response.
  const detail = await fetch(`${BASE}/api/assessments/${body.id}`).then((r) => r.json())
  expect(detail.verdict === body.verdict && detail.delta === body.delta,
    'detail after reload keeps the same verdict and Δ')
  const list = await fetch(`${BASE}/api/assessments`).then((r) => r.json())
  const listed = list.items.find((i) => i.id === body.id)
  expect(!!listed && listed.verdict === body.verdict,
    'list after reload keeps the same verdict')
  expect(typeof detail.formula.delta_line === 'string' &&
    detail.formula.delta_line.includes('Δ = Tg − Td'),
    'detail page exposes the substituted formula lines')

  // 3b. Traceable predecessor comparison for consecutive measurements of
  // the same voyage+hatch. The probe independently computes the expected
  // unrounded changes; the browser/API must not be trusted to subtract.
  const chainVoyage = 'ACCEPT-CHAIN'
  const m1 = { voyage: chainVoyage, hatch: '5H', tg: 25.345, ta: 20.123, rh: 71.5 }
  const m2 = { voyage: chainVoyage, hatch: '5H', tg: 24.117, ta: 21.987, rh: 68.25 }
  const c1 = await post(m1)
  const c2 = await post(m2)
  expect(c1.status === 201 && c2.status === 201, 'chain submissions are 201')
  expect(c1.body.comparison === undefined, 'POST body never carries a comparison block')
  expect(c2.body.comparison === undefined, 'POST body never carries a comparison block')

  const d1 = await fetch(`${BASE}/api/assessments/${c1.body.id}`).then((r) => r.json())
  expect(d1.comparison === undefined, 'first measurement has no comparison on detail either')

  // An interleaved submission for a DIFFERENT hatch must not enter the chain.
  const interleaved = await post({ voyage: chainVoyage, hatch: '6H', tg: 30, ta: 20, rh: 70 })
  expect(interleaved.status === 201, 'interleaved hatch submission is 201')

  const d2 = await fetch(`${BASE}/api/assessments/${c2.body.id}`).then((r) => r.json())
  expect(d2.comparison && d2.comparison.available === true, 'second measurement has an available comparison')
  expect(d2.comparison.previous.id === c1.body.id,
    `predecessor is the prior same-hatch record #${c1.body.id}, not the interleaved hatch`)
  expect(d2.comparison.previous.hatch === '5H', 'predecessor summary identifies the hatch')
  const g1 = Math.log(m1.rh / 100) + (A * m1.ta) / (B + m1.ta)
  const td1 = (B * g1) / (A - g1)
  const g2 = Math.log(m2.rh / 100) + (A * m2.ta) / (B + m2.ta)
  const td2 = (B * g2) / (A - g2)
  const expectedChanges = {
    tg: m2.tg - m1.tg,
    ta: m2.ta - m1.ta,
    rh: m2.rh - m1.rh,
    td: td2 - td1,
    delta: (m2.tg - td2) - (m1.tg - td1),
  }
  for (const [k, v] of Object.entries(expectedChanges)) {
    expect(Math.abs(d2.comparison.changes[k] - v) < 1e-12,
      `unrounded ${k} change matches independent math: api=${d2.comparison.changes[k]} expected=${v}`)
  }
  // The changes really are unrounded: at least one differs from its own 2-dp rounding.
  expect(Object.values(d2.comparison.changes).some((v) => round2(v) !== v),
    'comparison changes are delivered unrounded')
  const chainList = await fetch(`${BASE}/api/assessments`).then((r) => r.json())
  expect(chainList.items.every((i) => i.comparison === undefined),
    'list items never carry comparison blocks')

  // 3c. Voyage "hatch overview": one LATEST snapshot per hatch, grouped by
  // the server (MAX(id) per hatch), not filtered client-side. The probe
  // independently derives the expected latest id per hatch from the full
  // list, so a server that grouped on created_at or dropped a hatch fails.
  const ovVoyage = 'ACCEPT-OV'
  const ovOther = 'ACCEPT-OV-OTHER'
  // Interleave two voyages across the same hatch numbers; 2P measured 3×.
  const ovIds = []
  ovIds.push((await post({ voyage: ovVoyage, hatch: '2P', tg: 24, ta: 20, rh: 70 })).body.id)
  await post({ voyage: ovOther, hatch: '2P', tg: 5, ta: 28, rh: 95 })
  ovIds.push((await post({ voyage: ovVoyage, hatch: '3H', tg: 25, ta: 20, rh: 70 })).body.id)
  ovIds.push((await post({ voyage: ovVoyage, hatch: '2P', tg: 23, ta: 20, rh: 70 })).body.id)
  await post({ voyage: ovOther, hatch: '3H', tg: 6, ta: 28, rh: 95 })
  ovIds.push((await post({ voyage: ovVoyage, hatch: '2P', tg: 26, ta: 20, rh: 70 })).body.id)
  ovIds.push((await post({ voyage: ovVoyage, hatch: '4H', tg: 16.357, ta: 20, rh: 70 })).body.id)

  const overviewRes = await fetch(`${BASE}/api/voyages/${encodeURIComponent(ovVoyage)}/hatches/latest`)
  expect(overviewRes.status === 200, `overview is 200 (got ${overviewRes.status})`)
  const overview = await overviewRes.json()
  expect(overview.voyage === ovVoyage, 'overview echoes the voyage code')

  // Independent expectation from the full (newest-first) list.
  const fullList = (await fetch(`${BASE}/api/assessments`).then((r) => r.json())).items
  const expectedLatest = {}
  for (const it of fullList) {
    if (it.voyage === ovVoyage && !(it.hatch in expectedLatest)) {
      expectedLatest[it.hatch] = it // first seen per hatch is the latest
    }
  }
  const expectedHatches = Object.keys(expectedLatest).sort()
  expect(overview.items.length === expectedHatches.length,
    `one snapshot per hatch: ${overview.items.length} == ${expectedHatches.length}`)
  expect(overview.items.map((i) => i.hatch).join(',') === expectedHatches.join(','),
    `rows sorted by hatch: ${overview.items.map((i) => i.hatch).join(',')}`)
  for (const row of overview.items) {
    expect(row.id === expectedLatest[row.hatch].id,
      `${row.hatch} latest is record #${expectedLatest[row.hatch].id} (MAX id), got #${row.id}`)
    expect(row.voyage === ovVoyage, 'no row leaks from the interleaved other voyage')
    expect(row.comparison === undefined && row.formula === undefined,
      'snapshot rows are a lean read-only shape')
    // Values/verdict match the persisted latest record.
    const src = expectedLatest[row.hatch]
    expect(row.delta === src.delta && row.verdict === src.verdict,
      `${row.hatch} snapshot carries the latest record's own Δ and verdict`)
  }
  // The repeated hatch 2P resolves to the third (largest) id.
  const twoP = overview.items.find((i) => i.hatch === '2P')
  expect(twoP.id === Math.max(...ovIds.slice(0, 1), ovIds[2], ovIds[3]) && twoP.id === ovIds[3],
    'repeated measurements of 2P resolve to the newest record id')
  // Its detail link really serves that record.
  const linked = await fetch(`${BASE}/api/assessments/${twoP.id}`).then((r) => r.json())
  expect(linked.id === twoP.id && linked.hatch === '2P', 'a snapshot row links to its own detail')

  // The other voyage is grouped independently.
  const otherOv = await fetch(`${BASE}/api/voyages/${encodeURIComponent(ovOther)}/hatches/latest`).then((r) => r.json())
  expect(otherOv.items.length === 2 && otherOv.items.every((i) => i.voyage === ovOther),
    'the other voyage overview never shares rows despite equal hatch numbers')

  // Unknown voyage -> empty collection, not an error.
  const emptyOvRes = await fetch(`${BASE}/api/voyages/ACCEPT-OV-NO-SUCH/hatches/latest`)
  expect(emptyOvRes.status === 200, 'unknown voyage overview is still 200')
  const emptyOv = await emptyOvRes.json()
  expect(Array.isArray(emptyOv.items) && emptyOv.items.length === 0,
    'unknown voyage returns an empty items collection')

  // Malformed path percent-encoding -> explicit request error (400).
  const badEnc = await fetch(`${BASE}/api/voyages/a%zz/hatches/latest`)
  expect(badEnc.status === 400, `malformed path encoding is a 400 request error (got ${badEnc.status})`)

  // Existing POST/list/detail shapes are untouched by the new endpoint.
  const sampleDetail = await fetch(`${BASE}/api/assessments/${body.id}`).then((r) => r.json())
  expect(sampleDetail.formula && sampleDetail.comparison === undefined,
    'detail of the first measurement keeps formula and no comparison')

  // 4. Denied and retest zones.
  const denied = await post({ voyage: 'ACCEPT-2', hatch: '1P', tg: 5, ta: 28, rh: 95 })
  expect(denied.status === 201 && denied.body.verdict === 'denied',
    `cold grain / humid air -> denied (got ${denied.body?.verdict})`)

  const retest = await post({ voyage: 'ACCEPT-3', hatch: '2S', tg: 16.357, ta: 20, rh: 70 })
  expect(retest.status === 201 && retest.body.verdict === 'retest'
    && retest.body.delta_display === 2.00,
    `Δ displays 2.00 but is < 2 unrounded -> retest (got ${retest.body?.verdict}/${retest.body?.delta_display})`)
  const retestNeg = await post({ voyage: 'ACCEPT-4', hatch: '2S', tg: 12.36, ta: 20, rh: 70 })
  expect(retestNeg.status === 201 && retestNeg.body.verdict === 'retest'
    && retestNeg.body.delta_display === -2.00,
    `Δ displays -2.00 but is > -2 unrounded -> retest (got ${retestNeg.body?.verdict}/${retestNeg.body?.delta_display})`)

  // 5. Construct BOTH exact mathematical endpoints Δ = Td ± 2 against the
  // unrounded Td returned by the service. Floating-point addition may land
  // one ULP outside, so search the nearest Tg values: in both directions the
  // verdict must be retest at the inclusive endpoint and only change beyond.
  for (const dir of [1, -1]) {
    let tg = td + dir * 2
    const wanted = dir > 0 ? 'allowed' : 'denied'
    // Nudge inward until retest appears (the inclusive endpoint).
    for (let n = 0; n < 8; n++) {
      const r = await post({ voyage: `EDGE-${dir}-${n}`, hatch: 'H', tg, ta: 20, rh: 70 })
      if (r.body.verdict === 'retest') {
        expect(round2(r.body.delta) === dir * 2,
          `endpoint Δ=${dir * 2}.00 is retest (raw Δ=${r.body.delta})`)
        // One tiny step outward must already decide strictly.
        const step = 2e-12 * dir
        const out = await post({ voyage: `EDGE-OUT-${dir}-${n}`, hatch: 'H', tg: tg + step, ta: 20, rh: 70 })
        expect(out.body.verdict === wanted,
          `one ULP-scale step beyond Δ=${dir * 2}.00 -> ${wanted}`)
        break
      }
      tg -= dir * 5e-13
      if (n === 7) expect(false, `never found retest at endpoint dir=${dir}`)
    }
  }

  // 6. Invalid input -> 422 with field errors and NO new record.
  const before = (await fetch(`${BASE}/api/assessments`).then((r) => r.json())).items.length
  const cases = [
    { body: { voyage: 'BAD', hatch: 'H', tg: 60.5, ta: 20, rh: 70 }, field: 'tg' },
    { body: { voyage: 'BAD', hatch: 'H', tg: 20, ta: -20.01, rh: 70 }, field: 'ta' },
    { body: { voyage: 'BAD', hatch: 'H', tg: 20, ta: 20, rh: 0.99 }, field: 'rh' },
    { raw: '{"voyage":"BAD","hatch":"H","tg":1e999,"ta":20,"rh":70}', field: 'tg' },
    { body: { voyage: 'BAD', hatch: 'H', tg: -21, ta: 200, rh: 0 }, fields: 3 },
  ]
  for (const c of cases) {
    const r = c.raw ? await post(c.raw) : await post(c.body)
    expect(r.status === 422, `422 for rejected input (${c.field || c.fields})`)
    if (c.field) {
      expect(r.body.fields.some((f) => f.field === c.field), `field error names ${c.field}`)
    } else {
      expect(r.body.fields.length === c.fields, `all ${c.fields} bad fields reported`)
    }
  }
  const after = (await fetch(`${BASE}/api/assessments`).then((r) => r.json())).items.length
  expect(after === before, `no record persisted on 422 (${before} before, ${after} after)`)

  // 6b. Structural/format errors are 400 (malformed request body), not 422
  // field validation, and never persist a record.
  const structBefore = (await fetch(`${BASE}/api/assessments`).then((r) => r.json())).items.length
  const structCases = [
    { raw: '{"voyage":"STRUCT","hatch":"H","tg":25,"ta":20,"rh":70}]', hint: 'stray ]' },
    { raw: '{"voyage":"STRUCT","hatch":"H","tg":25,"ta":20,"rh":70} GARBAGE', hint: 'trailing text' },
    { raw: '{"voyage":"STRUCT","hatch":"H","tg":25,"ta":20,"rh":70,"rh":1}', hint: 'duplicate key' },
    { raw: 'null', hint: 'top-level null' },
    { raw: '[]', hint: 'top-level array' },
  ]
  for (const c of structCases) {
    const r = await post(c.raw)
    expect(r.status === 400, `${c.hint} is rejected as 400 format error (got ${r.status})`)
    expect(r.body && typeof r.body.error === 'string' && r.body.fields === undefined,
      `${c.hint} reports a body-level error without fabricated field errors`)
  }
  const nullCase = await post('null')
  expect(typeof nullCase.body.error === 'string' && nullCase.body.error.includes('格式错误'),
    'top-level null is explicitly diagnosed as a request body format error')
  const structAfter = (await fetch(`${BASE}/api/assessments`).then((r) => r.json())).items.length
  expect(structAfter === structBefore,
    `no record persisted on 400 (${structBefore} before, ${structAfter} after)`)

  console.log('\nALL ACCEPTANCE CHECKS PASSED')
}

main().catch((e) => {
  console.error(e)
  process.exit(1)
})
