import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import VerdictBadge from '@/components/VerdictBadge.vue'

function text(v) {
  return mount(VerdictBadge, { props: { verdict: v, hint: false } }).text()
}

describe('VerdictBadge', () => {
  it('maps the three API verdicts to Chinese labels', () => {
    expect(text('allowed')).toContain('允许通风')
    expect(text('denied')).toContain('禁止通风')
    expect(text('retest')).toContain('暂停并复测')
  })

  it('marks both band endpoints as retest', () => {
    // The endpoint discipline is enforced by the API; here we pin the
    // presentation so a retest response can never render as allowed/denied.
    const badge = mount(VerdictBadge, { props: { verdict: 'retest' } })
    expect(badge.classes()).toContain('v-retest')
    expect(badge.text()).toContain('含两端点')
  })
})
