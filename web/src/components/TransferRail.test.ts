import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import TransferRail from './TransferRail.vue'

const base = { phase: 'Copying to IMAP', discovered: 200, processed: 50, copied: 45, skipped: 4, failed: 1 }

describe('TransferRail', () => {
  it('describes determinate progress for assistive technology', () => {
    const w = mount(TransferRail, { props: base })
    expect(w.attributes('aria-label')).toBe('Copying to IMAP. 25 percent: 50 of 200 processed.')
  })

  it('says the total is unknown instead of showing a percentage when indeterminate', () => {
    const w = mount(TransferRail, { props: { ...base, indeterminate: true } })
    expect(w.attributes('aria-label')).toContain('Total not known yet')
    expect(w.attributes('aria-label')).not.toContain('percent')
    expect(w.find('.travel').exists()).toBe(true)
    expect(w.text()).toContain('counting…')
  })

  it('never exceeds 100 percent when more is processed than was found', () => {
    const w = mount(TransferRail, { props: { ...base, processed: 500 } })
    expect(w.attributes('aria-label')).toContain('100 percent')
  })

  it('sets failed messages aside only when there are some', () => {
    expect(mount(TransferRail, { props: { ...base, failed: 0 } }).find('.tray').exists()).toBe(false)
    const w = mount(TransferRail, { props: base })
    expect(w.find('.tray').text()).toContain('1 set aside')
    expect(w.find('.fail.some').exists()).toBe(true)
  })

  it('shows the current folder as an identifier', () => {
    const w = mount(TransferRail, { props: { ...base, currentFolder: 'Work/Customers' } })
    expect(w.find('.folder .mono').text()).toBe('Work/Customers')
  })
})
