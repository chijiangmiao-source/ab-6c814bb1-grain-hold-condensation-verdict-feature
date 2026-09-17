import { describe, expect, it, vi, afterEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import DetailPage from '@/pages/DetailPage.vue'

function record(over = {}) {
  return {
    id: 5, voyage: 'V-9', hatch: '1P',
    tg: 25, ta: 20, rh: 70,
    gamma: 0.9826379171132591, gamma_display: 0.98,
    td: 14.359183217771522, td_display: 14.36,
    delta: 10.640816782228478, delta_display: 10.64,
    verdict: 'allowed',
    formula: {
      gamma_line: 'γ = ln(70/100) + 17.62 × 20 / (243.12 + 20) = 0.982638（展示值 0.98）',
      td_line: 'Td = 243.12 × γ / (17.62 − γ) = 14.359183 ℃（展示值 14.36 ℃）',
      delta_line: 'Δ = Tg − Td = 25 − 14.359183 = 10.640817 ℃（展示值 10.64 ℃）',
      rule_line: '判定以未舍入 Δ 为准',
    },
    created_at: '2026-09-13T00:00:00Z',
    ...over,
  }
}

function mountWith(payload, status = 200) {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(
    new Response(JSON.stringify(payload), {
      status, headers: { 'Content-Type': 'application/json' },
    }),
  )
  return mount(DetailPage, {
    props: { id: '5' },
    global: { stubs: { RouterLink: true } },
  })
}

describe('DetailPage', () => {
  afterEach(() => vi.restoreAllMocks())

  it('lists every substituted formula line verbatim from the API', async () => {
    const w = mountWith(record())
    await flushPromises()

    const text = w.text()
    expect(text).toContain('ln(70/100)')
    expect(text).toContain('17.62 × 20 / (243.12 + 20)')
    expect(text).toContain('243.12 × γ / (17.62 − γ)')
    expect(text).toContain('Δ = Tg − Td = 25 − 14.359183')
    expect(text).toContain('10.640816782228478') // unrounded persisted value
    expect(text).toContain('允许通风')
  })

  it('shows the verdict next to a boundary-looking rounded delta without recomputing', async () => {
    const w = mountWith(record({ delta: 2.004, delta_display: 2.0, verdict: 'allowed' }))
    await flushPromises()
    expect(w.text()).toContain('2.004')
    expect(w.text()).toContain('允许通风')
    expect(w.text()).not.toContain('暂停并复测')
  })

  it('shows a first-measurement hint when the API returns no comparison', async () => {
    const w = mountWith(record())
    await flushPromises()
    const hint = w.find('[data-test=first-measurement]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toContain('首次测量')
    expect(w.find('[data-test=compare-card]').exists()).toBe(false)
    expect(w.find('[data-test=compare-unavailable]').exists()).toBe(false)
  })

  it('renders the comparison card with the predecessor summary and verbatim unrounded changes', async () => {
    // These change values are the API's own unrounded subtractions. They
    // cannot be obtained by subtracting the 2-dp display values, so if the
    // page ever recomputed them these exact strings would disappear.
    const payload = record({
      id: 8,
      tg: 24.117, ta: 21.987, rh: 68.25,
      td: 15.673342417319402, delta: 8.443657582680598,
      comparison: {
        available: true,
        previous: {
          id: 7, voyage: 'V-9', hatch: '1P',
          tg: 25.345, ta: 20.123, rh: 71.5,
          gamma: 1.051, td: 15.359183217771522,
          delta: 9.985816782228477,
          gamma_display: 1.05, td_display: 15.36, delta_display: 9.99,
          verdict: 'allowed',
          created_at: '2026-09-12T00:00:00Z',
        },
        // IEEE-754 subtraction of the two rows, as Go's float64 returns it;
        // delivered verbatim by the mocked API.
        changes: {
          tg: -1.228,
          ta: 1.864000000000001,
          rh: -3.25,
          td: 0.3141591995478805,
          delta: -1.5421591995478785,
        },
      },
    })
    const w = mountWith(payload)
    await flushPromises()

    const card = w.find('[data-test=compare-card]')
    expect(card.exists()).toBe(true)
    expect(w.find('[data-test=first-measurement]').exists()).toBe(false)

    const text = card.text()
    // Predecessor identity and verdict.
    expect(text).toContain('#7')
    expect(text).toContain('允许通风')
    // Unrounded persisted values of both rows.
    expect(text).toContain('15.359183217771522')
    expect(text).toContain('15.673342417319402')
    expect(text).toContain('9.985816782228477')
    // Change quantities verbatim from the API response.
    expect(text).toContain('-1.228')
    expect(text).toContain('1.864000000000001')
    expect(text).toContain('-3.25')
    expect(text).toContain('0.3141591995478805')
    expect(text).toContain('-1.5421591995478785')
    expect(text).toContain('粮温 Tg 变化')
    expect(text).toContain('露点 Td 变化（未舍入）')
    expect(text).toContain('温差 Δ 变化（未舍入）')
    // Positive changes get a leading plus for readability.
    expect(text).toContain('+')
  })

  it('marks a dangling predecessor comparison unavailable, without any card or rebind', async () => {
    const payload = record({
      comparison: {
        available: false,
        prev_id: 99,
        reason: '保存的前序记录已不存在，无法形成对照',
      },
    })
    const w = mountWith(payload)
    await flushPromises()

    const box = w.find('[data-test=compare-unavailable]')
    expect(box.exists()).toBe(true)
    expect(box.text()).toContain('前序对照不可用')
    expect(box.text()).toContain('#99')
    expect(box.text()).toContain('已不存在')
    expect(w.find('[data-test=compare-card]').exists()).toBe(false)
    expect(w.find('[data-test=first-measurement]').exists()).toBe(false)
    // The current assessment itself renders normally.
    expect(w.text()).toContain('允许通风')
    expect(w.text()).toContain('10.640816782228478')
  })

  it('marks a mismatched-voyage predecessor unavailable and shows the no-rebind note', async () => {
    const payload = record({
      comparison: {
        available: false,
        prev_id: 3,
        reason: '保存的前序记录不属于同一航次同一舱位，对照不可用；未临时改绑其他记录',
      },
    })
    const w = mountWith(payload)
    await flushPromises()
    const box = w.find('[data-test=compare-unavailable]')
    expect(box.text()).toContain('同一航次同一舱')
    expect(box.text()).toContain('未临时改绑')
    expect(w.find('[data-test=compare-card]').exists()).toBe(false)
  })

  it('handles 404', async () => {
    const w = mountWith({ error: '记录不存在' }, 404)
    await flushPromises()
    expect(w.text()).toContain('记录不存在')
  })
})
