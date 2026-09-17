// Thin client for the Gin API. The browser NEVER computes the dew point or
// verdict itself: every gamma/Td/delta/verdict value rendered on screen
// comes from these responses, so page and API share one calculation.
//
// Requests are same-origin (nginx proxies /api to Gin in Docker, and Vite's
// dev proxy does the same locally). Building an absolute URL from the
// document origin keeps the calls working under jsdom too.
const ORIGIN = typeof window !== 'undefined' && window.location
  ? window.location.origin
  : 'http://localhost'
const BASE = `${ORIGIN}/api`

async function parse(res) {
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  return { ok: res.ok, status: res.status, data }
}

export function listAssessments() {
  return fetch(`${BASE}/assessments`).then(parse)
}

export function getAssessment(id) {
  return fetch(`${BASE}/assessments/${encodeURIComponent(id)}`).then(parse)
}

// Read-only voyage overview: one latest snapshot per hatch, chosen by the
// server with MAX(id) per hatch. The browser receives the already-grouped
// items and only renders them; it never derives "latest" client-side.
export function getVoyageOverview(voyage) {
  return fetch(`${BASE}/voyages/${encodeURIComponent(voyage)}/hatches/latest`)
    .then(parse)
}

export async function createAssessment(payload) {
  const res = await fetch(`${BASE}/assessments`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  })
  return parse(res)
}

// Bulk entry for the chief officer's pre-berth multi-hatch readings. The
// server validates EVERY row with the same rules as createAssessment and
// saves all rows in one transaction; any invalid row rejects the whole
// batch. The browser only sends the ordered inputs and renders results —
// it never computes a dew point or chains predecessors itself.
export async function createAssessmentBatch(items) {
  const res = await fetch(`${BASE}/assessments/batch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ items }),
  })
  return parse(res)
}
