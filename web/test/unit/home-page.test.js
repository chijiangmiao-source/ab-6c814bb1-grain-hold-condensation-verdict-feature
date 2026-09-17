import { describe, expect, it, vi, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import HomePage from '@/pages/HomePage.vue'

function mountPage() {
  return mount(HomePage, { global: { stubs: { RouterLink: true } } })
}

async function fill(wrapper, values) {
  for (const [key, value] of Object.entries(values)) {
    await wrapper.find(`#f-${key}`).setValue(value)
  }
}

async function submit(wrapper) {
  await wrapper.find('button').trigger('submit.prevent')
  await flushPromises()
}

const VALID = { voyage: 'V-1', hatch: '3H', tg: '25', ta: '20', rh: '70' }

function batchDTO(overrides = {}) {
  return {
    id: 1, voyage: 'V-B', hatch: '3H',
    tg: 25, ta: 20, rh: 70,
    gamma: 0.9826, gamma_display: 0.98,
    td: 14.3591, td_display: 14.36,
    delta: 10.6408, delta_display: 10.64,
    verdict: 'allowed',
    formula: { gamma_line: 'g', td_line: 't', delta_line: 'd', rule_line: 'r' },
    created_at: '2026-09-13T00:00:00Z',
    ...overrides,
  }
}

async function switchToBatch(wrapper) {
  await wrapper.findAll('input[name=entry-mode]')[1].setValue()
}

async function fillBatch(wrapper, matrix) {
  // matrix: array of { voyage, hatch, tg, ta, rh } keyed by VISUAL row index.
  for (let i = 0; i < matrix.length; i++) {
    for (const [key, value] of Object.entries(matrix[i])) {
      await wrapper.find(`#bf-${i}-${key}`).setValue(String(value))
    }
  }
}

async function submitBatch(wrapper) {
  await wrapper.find('.batch-form').trigger('submit.prevent')
  await flushPromises()
}

describe('HomePage form', () => {
  afterEach(() => vi.restoreAllMocks())

  it('shows an inline error per out-of-range field and never calls POST', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[]}', { status: 200 }),
    )
    const w = mountPage()
    await flushPromises() // initial list load
    const fetchMock = vi.mocked(globalThis.fetch)
    fetchMock.mockClear()

    await fill(w, { voyage: 'V', hatch: 'H', tg: '60.5', ta: '20', rh: '120' })
    await submit(w)

    expect(fetchMock).not.toHaveBeenCalled()
    expect(w.find('#err-tg').text()).toContain('60.0')
    expect(w.find('#err-rh').text()).toContain('100.0')
    expect(w.find('#f-tg').attributes('aria-invalid')).toBe('true')
  })

  it('rejects blank and out-of-range numeric input inline', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[]}', { status: 200 }),
    )
    const w = mountPage()
    await flushPromises()

    await fill(w, { voyage: 'V', hatch: 'H', tg: '', ta: '-99', rh: '70' })
    await submit(w)

    expect(w.find('#err-tg').text()).toContain('必须填写')
    expect(w.find('#err-ta').text()).toContain('-20.0')
  })

  it('renders server 422 field errors under the matching fields', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      // POST -> 422 with a per-field error; GET list -> empty.
      if (init.method === 'POST') {
        return Promise.resolve(new Response(JSON.stringify({
          error: '输入校验失败，未生成任何记录',
          fields: [{ field: 'tg', code: 'not_finite', message: '粮温 Tg必须为有限数值' }],
        }), { status: 422, headers: { 'Content-Type': 'application/json' } }))
      }
      return Promise.resolve(new Response('{"items":[]}', { status: 200 }))
    })
    const w = mountPage()
    await flushPromises()

    await fill(w, VALID)
    await submit(w)

    expect(w.find('#err-tg').text()).toBe('粮温 Tg必须为有限数值')
    expect(w.find('.banner-error').text()).toContain('未生成任何记录')
    expect(w.find('.result').exists()).toBe(false)
  })

  it('renders exactly the API values; verdict is not re-derived from rounded Δ', async () => {
    // delta_display looks like the boundary (2.00), yet the unrounded delta
    // is 2.004 and the API says allowed. The page must show "allowed".
    const apiRecord = {
      id: 3, voyage: 'V-1', hatch: '3H',
      tg: 25, ta: 20, rh: 70,
      gamma: 0.9826379171132591, gamma_display: 0.98,
      td: 14.359183217771522, td_display: 14.36,
      delta: 2.004, delta_display: 2.0,
      verdict: 'allowed',
      formula: { gamma_line: 'g', td_line: 't', delta_line: 'd', rule_line: 'r' },
      created_at: '2026-09-13T00:00:00Z',
    }
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ items: [apiRecord] }), { status: 200 }),
    )
    const w = mountPage()
    await flushPromises()

    const cells = w.findAll('tbody td').map((td) => td.text())
    expect(cells).toContain('2.00')
    expect(w.text()).toContain('允许通风')
    expect(w.text()).not.toContain('暂停并复测')
  })

  it('posts the parsed numeric form and shows the returned result on success', async () => {    const created = {
      id: 11, voyage: 'V-1', hatch: '3H',
      tg: 25, ta: 20, rh: 70,
      gamma: 0.9826, gamma_display: 0.98,
      td: 14.3591, td_display: 14.36,
      delta: 10.6408, delta_display: 10.64,
      verdict: 'allowed',
      formula: { gamma_line: 'g', td_line: 't', delta_line: 'd', rule_line: 'r' },
      created_at: '2026-09-13T00:00:00Z',
    }
    const postCalls = []
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      if (init.method === 'POST') {
        postCalls.push(JSON.parse(init.body))
        return Promise.resolve(new Response(JSON.stringify(created), {
          status: 201, headers: { 'Content-Type': 'application/json' },
        }))
      }
      return Promise.resolve(new Response(JSON.stringify({ items: [created] }), { status: 200 }))
    })

    const w = mountPage()
    await flushPromises()
    await fill(w, VALID)
    await submit(w)

    expect(postCalls).toEqual([{ voyage: 'V-1', hatch: '3H', tg: 25, ta: 20, rh: 70 }])
    expect(w.find('.result').exists()).toBe(true)
    expect(w.find('.result').text()).toContain('10.64')
    expect(w.find('.result').text()).toContain('允许通风')
  })

  it('offers one overview entry per distinct voyage, with an encoded link', async () => {
    const items = [
      { id: 3, voyage: 'V-1', hatch: '3H', tg: 25, ta: 20, rh: 70, delta_display: 10.64, verdict: 'allowed' },
      { id: 2, voyage: 'V-2', hatch: '1H', tg: 5, ta: 28, rh: 95, delta_display: -22, verdict: 'denied' },
      { id: 1, voyage: 'V-1', hatch: '2P', tg: 24, ta: 20, rh: 70, delta_display: 9.64, verdict: 'allowed' },
    ]
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ items }), { status: 200 }),
    )

    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: HomePage },
        { path: '/voyages/:voyage/hatches/latest', component: { template: '<div/>' } },
        { path: '/assessments/:id', component: { template: '<div/>' } },
      ],
    })
    const w = mount(HomePage, { global: { plugins: [router] } })
    await flushPromises()

    const entries = w.findAll('[data-test=voyage-entry]')
    // V-1 appears twice in history but yields only one entry.
    expect(entries.map((a) => a.text())).toEqual(['V-1 →', 'V-2 →'])
    expect(entries[0].attributes('href')).toBe('/voyages/V-1/hatches/latest')

    // A voyage containing a slash is percent-encoded in the link.
    const slashItem = { id: 4, voyage: 'V/A', hatch: '9H', tg: 25, ta: 20, rh: 70, delta_display: 10.64, verdict: 'allowed' }
    vi.mocked(globalThis.fetch).mockResolvedValue(
      new Response(JSON.stringify({ items: [slashItem, ...items] }), { status: 200 }),
    )
    const router2 = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: HomePage },
        { path: '/voyages/:voyage/hatches/latest', component: { template: '<div/>' } },
        { path: '/assessments/:id', component: { template: '<div/>' } },
      ],
    })
    const w2 = mount(HomePage, { global: { plugins: [router2] } })
    await flushPromises()
    const slashLink = w2.findAll('[data-test=voyage-entry]').find((a) => a.text().includes('V/A'))
    expect(slashLink.attributes('href')).toBe('/voyages/V%2FA/hatches/latest')
  })

  // ===================== 批量抄录模式 =====================

  it('batch mode posts an ordered measurements array and shows per-row results', async () => {
    const results = [
      batchDTO({ id: 21, hatch: '3H', delta_display: 10.64, verdict: 'allowed' }),
      batchDTO({ id: 22, hatch: '4H', delta_display: 9.64, verdict: 'allowed' }),
      batchDTO({ id: 23, hatch: '3H', delta_display: 8.64, verdict: 'allowed' }),
    ]
    const calls = []
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      if (init.method === 'POST') {
        calls.push({ url: String(url), body: JSON.parse(init.body) })
        return Promise.resolve(new Response(JSON.stringify({ items: results }), {
          status: 201, headers: { 'Content-Type': 'application/json' },
        }))
      }
      return Promise.resolve(new Response(JSON.stringify({ items: results }), { status: 200 }))
    })

    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: HomePage },
        { path: '/assessments/:id', component: { template: '<div/>' } },
        { path: '/voyages/:voyage/hatches/latest', component: { template: '<div/>' } },
      ],
    })
    const w = mount(HomePage, { global: { plugins: [router] } })
    await flushPromises()
    await switchToBatch(w)

    await fillBatch(w, [
      { voyage: 'V-B', hatch: '3H', tg: 25, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '4H', tg: 24, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '3H', tg: 23, ta: 20, rh: 70 },
    ])
    await submitBatch(w)

    expect(calls).toHaveLength(1)
    expect(calls[0].url).toMatch(/\/api\/assessments\/batch$/)
    expect(calls[0].body).toEqual({ measurements: [
      { voyage: 'V-B', hatch: '3H', tg: 25, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '4H', tg: 24, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '3H', tg: 23, ta: 20, rh: 70 },
    ] })

    const resultRows = w.findAll('[data-test=batch-result-row]')
    expect(resultRows).toHaveLength(3)
    expect(resultRows[0].text()).toContain('#21')
    expect(resultRows[2].text()).toContain('#23')
    // Each row links to the existing detail route.
    expect(w.findAll('[data-test=batch-result-row] a').map((a) => a.attributes('href')))
      .toEqual(['/assessments/21', '/assessments/22', '/assessments/23'])
  })

  it('keeps all inputs on a row-numbered 422 and marks the problem row/field', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      if (init.method === 'POST') {
        // Row 2 (1-based) has an out-of-range tg.
        return Promise.resolve(new Response(JSON.stringify({
          error: '批量输入校验失败，未生成任何记录',
          fields: [{ row: 2, field: 'tg', code: 'out_of_range', message: '粮温 Tg必须在 -20.0 至 60.0 之间' }],
        }), { status: 422, headers: { 'Content-Type': 'application/json' } }))
      }
      return Promise.resolve(new Response('{"items":[]}', { status: 200 }))
    })

    const w = mountPage()
    await flushPromises()
    await switchToBatch(w)
    await fillBatch(w, [
      { voyage: 'V-B', hatch: '3H', tg: 25, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '4H', tg: 24, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '5H', tg: 23, ta: 20, rh: 70 },
    ])
    await submitBatch(w)

    // No result card, banner explains the rejection.
    expect(w.find('[data-test=batch-result]').exists()).toBe(false)
    expect(w.find('[data-test=batch-banner-error]').text()).toContain('整批未保存')

    // Only row 2 is marked invalid; its field error is shown in place.
    const invalidRows = w.findAll('.batch-row.row-invalid')
    expect(invalidRows).toHaveLength(1)
    expect(invalidRows[0].attributes('id')).toBe('batch-row-1')
    expect(w.find('#berr-1-tg').text()).toContain('60.0')
    expect(w.find('#bf-1-tg').attributes('aria-invalid')).toBe('true')

    // Every input value is retained.
    expect(w.find('#bf-0-hatch').element.value).toBe('3H')
    expect(w.find('#bf-1-hatch').element.value).toBe('4H')
    expect(w.find('#bf-1-tg').element.value).toBe('24')
    expect(w.find('#bf-2-hatch').element.value).toBe('5H')

    // Nothing was added to history (GET list is empty in this mock).
    expect(w.findAll('.history tbody tr')).toHaveLength(0)
  })

  it('maps a server row number to the visual row across blank lines', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      if (init.method === 'POST') {
        // Submitted array row 1 is visual row 1 (row 0 is left blank).
        return Promise.resolve(new Response(JSON.stringify({
          error: '批量输入校验失败，未生成任何记录',
          fields: [{ row: 1, field: 'rh', code: 'out_of_range', message: '相对湿度 RH必须在 1.0 至 100.0 之间' }],
        }), { status: 422, headers: { 'Content-Type': 'application/json' } }))
      }
      return Promise.resolve(new Response('{"items":[]}', { status: 200 }))
    })

    const w = mountPage()
    await flushPromises()
    await switchToBatch(w)
    // Visual row 0 stays empty; fill rows 1 and 2.
    await fillBatch(w, [
      { voyage: '', hatch: '', tg: '', ta: '', rh: '' },
      { voyage: 'V-B', hatch: '4H', tg: 24, ta: 20, rh: 120 },
      { voyage: 'V-B', hatch: '5H', tg: 23, ta: 20, rh: 70 },
    ])
    await submitBatch(w)

    // The error lands on visual row 1 (the first SUBMITTED row), not row 0.
    expect(w.find('#batch-row-1').classes()).toContain('row-invalid')
    expect(w.find('#berr-1-rh').text()).toContain('100.0')
  })

  it('validates client-side per row and never POSTs a locally invalid batch', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[]}', { status: 200 }),
    )
    const w = mountPage()
    await flushPromises()
    fetchMock.mockClear()
    await switchToBatch(w)

    await fillBatch(w, [
      { voyage: 'V-B', hatch: '3H', tg: 999, ta: 20, rh: 70 }, // row 1 bad tg
      { voyage: 'V-B', hatch: '4H', tg: 24, ta: 20, rh: 70 },
    ])
    await submitBatch(w)

    expect(fetchMock).not.toHaveBeenCalled()
    expect(w.find('#berr-0-tg').text()).toContain('60.0')
    expect(w.find('.batch-row.row-invalid').attributes('id')).toBe('batch-row-0')
    expect(w.find('[data-test=batch-banner-error]').text()).toContain('1 行')
  })

  it('removing a row re-maps row-keyed errors so highlights follow their rows', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[]}', { status: 200 }),
    )
    const w = mountPage()
    await flushPromises()
    fetchMock.mockClear()
    await switchToBatch(w)

    // Row 0 is fine; rows 1 and 2 are invalid (tg out of range).
    await fillBatch(w, [
      { voyage: 'V-B', hatch: '3H', tg: 25, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '4H', tg: 999, ta: 20, rh: 70 },
      { voyage: 'V-B', hatch: '5H', tg: 998, ta: 20, rh: 70 },
    ])
    await submitBatch(w)
    expect(w.findAll('.batch-row.row-invalid').map((tr) => tr.attributes('id')))
      .toEqual(['batch-row-1', 'batch-row-2'])

    // Delete the valid first row: the two errors must shift down together.
    await w.findAll('.btn-remove')[0].trigger('click')
    expect(w.findAll('.batch-row')).toHaveLength(4)
    expect(w.findAll('.batch-row.row-invalid').map((tr) => tr.attributes('id')))
      .toEqual(['batch-row-0', 'batch-row-1'])
    expect(w.find('#berr-0-tg').exists()).toBe(true)
    expect(w.find('#berr-1-tg').exists()).toBe(true)
  })

  it('refuses an all-empty batch and can grow up to twenty rows', async () => {    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[]}', { status: 200 }),
    )
    const w = mountPage()
    await flushPromises()
    await switchToBatch(w)

    // Starts with five rows.
    expect(w.findAll('.batch-row')).toHaveLength(5)

    await submitBatch(w)
    expect(w.find('[data-test=batch-banner-error]').text()).toContain('至少填写一行')

    // Grow to the limit; the add button then disables.
    for (let n = 5; n < 20; n++) {
      await w.find('.btn-add').trigger('click')
    }
    expect(w.findAll('.batch-row')).toHaveLength(20)
    expect(w.find('.btn-add').attributes('disabled')).toBeDefined()

    // Removing a row brings the count back down.
    await w.findAll('.btn-remove')[19].trigger('click')
    expect(w.findAll('.batch-row')).toHaveLength(19)
  })
})
