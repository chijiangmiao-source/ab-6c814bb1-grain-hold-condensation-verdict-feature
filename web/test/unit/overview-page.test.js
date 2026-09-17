import { describe, expect, it, vi, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import OverviewPage from '@/pages/OverviewPage.vue'

function snapshot(over = {}) {
  return {
    id: 3, voyage: 'V-1', hatch: '3H',
    tg: 25, ta: 20, rh: 70,
    gamma: 0.9826379171132591, gamma_display: 0.98,
    td: 14.359183217771522, td_display: 14.36,
    delta: 2.004, delta_display: 2.0,
    verdict: 'allowed',
    created_at: '2026-09-13T08:00:00Z',
    ...over,
  }
}

// Real router (not a stubbed RouterLink) so each row's <a href> is a true
// navigable link to that record's detail.
function mountOverview(voyage, payload, status = 200) {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response(JSON.stringify(payload), {
      status, headers: { 'Content-Type': 'application/json' },
    }),
  )
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div/>' } },
      { path: '/assessments/:id', component: { template: '<div/>' } },
      { path: '/voyages/:voyage/hatches/latest', component: OverviewPage, props: true },
    ],
  })
  return mount(OverviewPage, {
    props: { voyage },
    global: { plugins: [router] },
  })
}

function rows(wrapper) {
  return wrapper.findAll('[data-test=overview-row]').map((tr) =>
    tr.findAll('td').map((td) => td.text()))
}

describe('OverviewPage', () => {
  afterEach(() => vi.restoreAllMocks())

  it('requests the per-voyage endpoint with the voyage encoded', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ voyage: 'V/1', items: [] }), { status: 200 }),
    )
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/', component: { template: '<div/>' } },
        { path: '/voyages/:voyage/hatches/latest', component: OverviewPage, props: true },
      ],
    })
    mount(OverviewPage, {
      props: { voyage: 'V/1' },
      global: { plugins: [router] },
    })
    await flushPromises()

    expect(String(fetchMock.mock.calls[0][0])).toMatch(
      /\/api\/voyages\/V%2F1\/hatches\/latest$/)
  })

  it('renders exactly the API items in API order, with unrounded values and server verdict', async () => {
    // The server already grouped to one MAX(id) row per hatch and sorted by
    // hatch. The page must render these verbatim: the "3H" row has a display
    // delta of exactly 2.00 but an unrounded 2.004 and verdict "allowed" —
    // a client re-deriving the verdict from the rounded value would show
    // "retest". The page must show the API's "allowed".
    const items = [
      snapshot({ id: 21, hatch: '2P', delta: -22.0, delta_display: -22, verdict: 'denied',
        created_at: '2026-09-13T07:00:00Z' }),
      snapshot({ id: 24, hatch: '3H' }), // latest 3H (id 24, not an earlier row)
      snapshot({ id: 23, hatch: '4H', delta: 10.64, delta_display: 10.64, verdict: 'allowed' }),
    ]
    const w = mountOverview('V-1', { voyage: 'V-1', items })
    await flushPromises()

    // Header echoes the server voyage value.
    expect(w.find('[data-test=overview]').text()).toContain('航次 V-1')

    const tableRows = rows(w)
    expect(tableRows).toHaveLength(3)
    // Server order is preserved exactly (no client-side re-sorting).
    expect(tableRows.map((r) => r[0])).toEqual(['2P', '3H', '4H'])
    // "Latest assessment" column is the server record id.
    expect(tableRows.map((r) => r[1])).toEqual(['#21', '#24', '#23'])

    const threeHRow = w.findAll('[data-test=overview-row]')[1]
    expect(tableRows[1][3]).toBe('2.004') // unrounded delta verbatim
    expect(tableRows[1][4]).toBe('2.00') // display delta
    expect(threeHRow.text()).toContain('允许通风')
    expect(threeHRow.text()).not.toContain('暂停并复测')

    // Measurement time is the API created_at (2026 date), not the page's clock.
    for (const r of tableRows) {
      expect(r[2]).toMatch(/2026/)
    }
  })

  it('links each row to that server record detail and offers a way back to history', async () => {
    const items = [
      snapshot({ id: 31, hatch: '1H' }),
      snapshot({ id: 47, hatch: '2H' }),
    ]
    const w = mountOverview('V-9', { voyage: 'V-9', items })
    await flushPromises()

    const hrefs = w.findAll('[data-test=overview-row] a').map((a) => a.attributes('href'))
    expect(hrefs).toEqual(['/assessments/31', '/assessments/47'])

    const back = w.findAll('a').filter((a) => a.text().includes('返回历史区'))
    expect(back.length).toBeGreaterThan(0)
    expect(back[0].attributes('href')).toBe('/')
  })

  it('shows an explicit empty state for a voyage with no records', async () => {
    const w = mountOverview('V-NONE', { voyage: 'V-NONE', items: [] })
    await flushPromises()

    expect(w.find('[data-test=overview-table]').exists()).toBe(false)
    expect(w.find('[data-test=overview-empty]').exists()).toBe(true)
    expect(w.find('[data-test=overview-empty]').text()).toContain('暂无')
  })

  it('renders a request error (e.g. malformed path encoding) and keeps the back-to-history entry', async () => {
    const w = mountOverview('a%zz', { error: '请求路径错误：非法的路径编码' }, 400)
    await flushPromises()

    const box = w.find('[data-test=overview-error]')
    expect(box.exists()).toBe(true)
    expect(box.text()).toContain('非法的路径编码')
    expect(box.find('a').attributes('href')).toBe('/')
    expect(box.text()).toContain('返回历史区')
    expect(w.find('[data-test=overview]').exists()).toBe(false)
  })
})
