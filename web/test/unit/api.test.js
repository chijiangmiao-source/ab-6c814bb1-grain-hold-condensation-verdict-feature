import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createAssessment, createAssessmentBatch, getAssessment, getVoyageOverview, listAssessments } from '@/lib/api.js'

describe('api client', () => {
  afterEach(() => vi.restoreAllMocks())

  it('posts JSON and returns parsed 201 body', async () => {
    const payload = { voyage: 'V1', hatch: '2', tg: 25, ta: 20, rh: 70 }
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ id: 7, verdict: 'allowed', delta_display: 10.64 }), {
        status: 201, headers: { 'Content-Type': 'application/json' },
      }),
    )

    const res = await createAssessment(payload)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toMatch(/^https?:\/\/[^/]+\/api\/assessments$/)
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body)).toEqual(payload)
    expect(res.status).toBe(201)
    expect(res.ok).toBe(true)
    expect(res.data.verdict).toBe('allowed')
  })

  it('surfaces 422 field errors without throwing', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({
        error: '输入校验失败，未生成任何记录',
        fields: [{ field: 'tg', code: 'out_of_range', message: '粮温 Tg必须在 -20.0 至 60.0 之间' }],
      }), { status: 422, headers: { 'Content-Type': 'application/json' } }),
    )

    const res = await createAssessment({ voyage: 'v', hatch: 'h', tg: 99, ta: 20, rh: 70 })
    expect(res.ok).toBe(false)
    expect(res.status).toBe(422)
    expect(res.data.fields[0].field).toBe('tg')
  })

  it('builds list and detail urls', async () => {
    const calls = []
    vi.spyOn(globalThis, 'fetch').mockImplementation((url) => {
      calls.push(String(url))
      return Promise.resolve(new Response('{"items":[]}', { status: 200 }))
    })
    await listAssessments()
    await getAssessment('42')
    expect(calls.map(String)).toEqual([
      expect.stringMatching(/\/api\/assessments$/),
      expect.stringMatching(/\/api\/assessments\/42$/),
    ])
  })

  it('builds the per-voyage overview url with an encoded voyage and returns grouped items', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({
        voyage: 'V/A',
        items: [{ id: 9, hatch: '3H', verdict: 'allowed', delta: 10.64 }],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } }),
    )

    const res = await getVoyageOverview('V/A')

    expect(String(fetchMock.mock.calls[0][0])).toMatch(
      /\/api\/voyages\/V%2FA\/hatches\/latest$/)
    expect(res.status).toBe(200)
    expect(res.data.items[0].id).toBe(9)
    expect(res.data.items[0].verdict).toBe('allowed')
  })

  it('posts a batch wrapped in a measurements array and returns row items', async () => {
    const measurements = [
      { voyage: 'V1', hatch: '3H', tg: 25, ta: 20, rh: 70 },
      { voyage: 'V1', hatch: '4H', tg: 24, ta: 20, rh: 70 },
    ]
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ items: [{ id: 1 }, { id: 2 }] }), {
        status: 201, headers: { 'Content-Type': 'application/json' },
      }),
    )

    const res = await createAssessmentBatch(measurements)

    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toMatch(/\/api\/assessments\/batch$/)
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body)).toEqual({ measurements })
    expect(res.status).toBe(201)
    expect(res.data.items.map((i) => i.id)).toEqual([1, 2])
  })

  it('returns row-numbered 422 field errors for a batch without throwing', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({
        error: '批量输入校验失败，未生成任何记录',
        fields: [{ row: 2, field: 'tg', code: 'out_of_range', message: 'x' }],
      }), { status: 422, headers: { 'Content-Type': 'application/json' } }),
    )

    const res = await createAssessmentBatch([
      { voyage: 'v', hatch: 'h', tg: 1, ta: 20, rh: 70 },
      { voyage: 'v', hatch: 'h', tg: 999, ta: 20, rh: 70 },
    ])
    expect(res.status).toBe(422)
    expect(res.data.fields[0].row).toBe(2)
    expect(res.data.fields[0].field).toBe('tg')
  })
})
