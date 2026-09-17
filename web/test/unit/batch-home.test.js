import { describe, expect, it, vi, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import HomePage from '@/pages/HomePage.vue'

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: HomePage },
      { path: '/assessments/:id', component: { template: '<div/>' } },
      { path: '/voyages/:voyage/hatches/latest', component: { template: '<div/>' } },
    ],
  })
}

async function mountBatch({ router } = {}) {
  const wrapper = mount(HomePage, {
    global: router
      ? { plugins: [router] }
      : { stubs: { RouterLink: true } },
  })
  await flushPromises() // initial list load
  await wrapper.findAll('button[role=tab]')[1].trigger('click')
  return wrapper
}

async function fillRow(wrapper, i, values) {
  for (const [key, value] of Object.entries(values)) {
    await wrapper.find(`#bf-${i}-${key}`).setValue(String(value))
  }
}

async function addRow(wrapper) {
  await wrapper.find('[data-test=batch-add-row]').trigger('click')
}

async function submitBatch(wrapper) {
  await wrapper.find('.batch-form').trigger('submit.prevent')
  await flushPromises()
}

function savedRow(id, patch = {}) {
  return {
    id,
    voyage: 'V-B', hatch: '1H',
    tg: 25, ta: 20, rh: 70,
    gamma: 0.9826, gamma_display: 0.98,
    td: 14.3591, td_display: 14.36,
    delta: 10.6408, delta_display: 10.64,
    verdict: 'allowed',
    formula: { gamma_line: 'g', td_line: 't', delta_line: 'd', rule_line: 'r' },
    created_at: '2026-09-13T00:00:00Z',
    ...patch,
  }
}

describe('HomePage batch mode', () => {
  afterEach(() => vi.restoreAllMocks())

  it('starts in single mode and switches to a one-row batch form', async () => {
    const wrapper = await mountBatch()
    expect(wrapper.find('.batch-form').exists()).toBe(true)
    expect(wrapper.findAll('[data-test=batch-row]')).toHaveLength(1)
  })

  it('adds rows up to 20 and removes a row', async () => {
    const wrapper = await mountBatch()

    // The only row cannot be removed.
    expect(wrapper.find('[data-test=batch-remove-row]').attributes('disabled')).toBeDefined()

    for (let n = 0; n < 4; n++) await addRow(wrapper)
    expect(wrapper.findAll('[data-test=batch-row]')).toHaveLength(5)

    await wrapper.findAll('[data-test=batch-remove-row]')[2].trigger('click')
    expect(wrapper.findAll('[data-test=batch-row]')).toHaveLength(4)

    // The add button caps at twenty rows.
    const btn = wrapper.find('[data-test=batch-add-row]')
    for (let n = wrapper.findAll('[data-test=batch-row]').length; n < 20; n++) await addRow(wrapper)
    expect(wrapper.findAll('[data-test=batch-row]')).toHaveLength(20)
    expect(btn.attributes('disabled')).toBeDefined()
  })

  it('validates every row inline, marks the row, and never calls the API', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response('{"items":[]}', { status: 200 }),
    )
    const wrapper = await mountBatch()
    await addRow(wrapper)

    await fillRow(wrapper, 0, { voyage: 'V', hatch: '1H', tg: '25', ta: '20', rh: '70' })
    // Row 2: tg out of range and rh missing.
    await fillRow(wrapper, 1, { voyage: 'V', hatch: '2H', tg: '999', ta: '20', rh: '' })

    const fetchMock = vi.mocked(globalThis.fetch)
    fetchMock.mockClear()
    await submitBatch(wrapper)

    expect(fetchMock).not.toHaveBeenCalled()
    expect(wrapper.find('#batch-row-1').classes()).toContain('row-invalid')
    expect(wrapper.find('#berr-1-tg').text()).toContain('60.0')
    expect(wrapper.find('#berr-1-rh').text()).toContain('必须填写')
    // The valid row is not marked.
    expect(wrapper.find('#batch-row-0').classes()).not.toContain('row-invalid')
  })

  it('posts the ordered rows wrapped in items and renders per-row results', async () => {
    const saved = [
      savedRow(31, { hatch: '1H', delta: 10.6408, delta_display: 10.64, verdict: 'allowed' }),
      savedRow(32, { hatch: '2P', tg: 5, ta: 28, rh: 95, delta: -22, delta_display: -22, verdict: 'denied' }),
      savedRow(33, { hatch: '1H', tg: 26, delta: 11.6408, delta_display: 11.64, verdict: 'allowed' }),
    ]
    const postCalls = []
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      if (init.method === 'POST') {
        postCalls.push(JSON.parse(init.body))
        return Promise.resolve(new Response(JSON.stringify({ items: saved }), {
          status: 201, headers: { 'Content-Type': 'application/json' },
        }))
      }
      return Promise.resolve(new Response(JSON.stringify({ items: [...saved].reverse() }), { status: 200 }))
    })

    const wrapper = await mountBatch({ router: makeRouter() })
    await addRow(wrapper)
    await addRow(wrapper)
    await fillRow(wrapper, 0, { voyage: 'V-B', hatch: '1H', tg: '25', ta: '20', rh: '70' })
    await fillRow(wrapper, 1, { voyage: 'V-B', hatch: '2P', tg: '5', ta: '28', rh: '95' })
    await fillRow(wrapper, 2, { voyage: 'V-B', hatch: '1H', tg: '26', ta: '20', rh: '70' })
    await submitBatch(wrapper)

    expect(postCalls).toEqual([{
      items: [
        { voyage: 'V-B', hatch: '1H', tg: 25, ta: 20, rh: 70 },
        { voyage: 'V-B', hatch: '2P', tg: 5, ta: 28, rh: 95 },
        { voyage: 'V-B', hatch: '1H', tg: 26, ta: 20, rh: 70 },
      ],
    }])

    const resultRows = wrapper.findAll('[data-test=batch-result-row]')
    expect(resultRows).toHaveLength(3)
    // Submission order preserved, each with its id, delta and verdict.
    expect(resultRows[0].text()).toContain('#31')
    expect(resultRows[0].text()).toContain('10.64')
    expect(resultRows[1].text()).toContain('#32')
    expect(resultRows[1].text()).toContain('禁止通风')
    expect(resultRows[2].text()).toContain('#33')

    // History refreshed with the new records (server returns newest first).
    const historyRows = wrapper.findAll('.history tbody tr')
    expect(historyRows[0].text()).toContain('33')
  })

  it('renders a server 422 under the named row, keeps all inputs and shows no result', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      if (init.method === 'POST') {
        // Client-side ranges currently pass, but the server rejects row 2
        // (e.g. a future rule change): the page must render the API's
        // row-numbered error verbatim.
        return Promise.resolve(new Response(JSON.stringify({
          error: '批量输入校验失败，整批未保存（共 1 处字段错误）',
          rows: [{ row: 2, fields: [{ field: 'tg', code: 'out_of_range', message: '粮温 Tg必须在 -20.0 至 60.0 之间' }] }],
          fields: [{ row: 2, field: 'tg', code: 'out_of_range', message: '粮温 Tg必须在 -20.0 至 60.0 之间' }],
        }), { status: 422, headers: { 'Content-Type': 'application/json' } }))
      }
      return Promise.resolve(new Response('{"items":[]}', { status: 200 }))
    })

    const wrapper = await mountBatch()
    await addRow(wrapper)
    await fillRow(wrapper, 0, { voyage: 'V-B', hatch: '1H', tg: '25', ta: '20', rh: '70' })
    await fillRow(wrapper, 1, { voyage: 'V-B', hatch: '2H', tg: '25', ta: '20', rh: '70' })
    await submitBatch(wrapper)

    // All inputs are retained exactly as entered.
    expect(wrapper.find('#bf-1-tg').element.value).toBe('25')
    expect(wrapper.find('#bf-0-voyage').element.value).toBe('V-B')
    // Error is attached to row 2 (index 1), row 1 stays clean.
    expect(wrapper.find('#batch-row-1').classes()).toContain('row-invalid')
    expect(wrapper.find('#berr-1-tg').text()).toContain('60.0')
    expect(wrapper.find('#batch-row-0').classes()).not.toContain('row-invalid')
    expect(wrapper.find('[data-test=batch-error]').text()).toContain('整批未保存')
    expect(wrapper.find('[data-test=batch-result]').exists()).toBe(false)
  })

  it('reports a non-422 failure without clearing the entered rows', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, init = {}) => {
      if (init.method === 'POST') {
        return Promise.resolve(new Response(JSON.stringify({ error: '批量保存失败，已全部回滚: boom' }), {
          status: 500, headers: { 'Content-Type': 'application/json' },
        }))
      }
      return Promise.resolve(new Response('{"items":[]}', { status: 200 }))
    })

    const wrapper = await mountBatch()
    await fillRow(wrapper, 0, { voyage: 'V-B', hatch: '1H', tg: '25', ta: '20', rh: '70' })
    await submitBatch(wrapper)

    expect(wrapper.find('[data-test=batch-error]').text()).toContain('已全部回滚')
    expect(wrapper.find('#bf-0-hatch').element.value).toBe('1H')
  })
})
