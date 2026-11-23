import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { LabelView } from '../services/client'
import LabelPreviewList from './LabelPreviewList.vue'

const label = (o: Partial<LabelView>): LabelView => ({ id: 'L', name: 'Label', system: false, excluded: false, folder: 'Label', collides_with: null, ...o })

describe('LabelPreviewList', () => {
  const labels = [
    label({ id: 'INBOX', name: 'INBOX', system: true, folder: 'INBOX' }),
    label({ id: 'SPAM', name: 'SPAM', system: true, excluded: true, folder: '' }),
    label({ id: 'STARRED', name: 'STARRED', system: true, folder: '' }),
    label({ id: 'a', name: 'Work', folder: 'Work', collides_with: ['b'] }),
    label({ id: 'b', name: 'work', folder: 'Work', collides_with: ['a'] }),
    label({ id: 'c', name: 'Projects', folder: 'Clients/Projects' }),
  ]
  const mount_ = () => mount(LabelPreviewList, { props: { labels, mapped: { c: 'Clients/Projects' } } })

  it('shows the resulting folder, "Not copied" for excluded labels and a flags-only note', () => {
    const w = mount_()
    const row = (id: string) => w.find(`[data-label="${id}"]`)
    expect(row('INBOX').find('[data-testid=folder]').text()).toBe('INBOX')
    expect(row('SPAM').text()).toContain('Not copied')
    expect(row('STARRED').text()).toContain('No folder (sets flags only)')
    expect(row('c').text()).toContain('Renamed')
  })

  it('flags labels that collide on one folder', () => {
    const w = mount_()
    expect(w.findAll('[data-testid=collision]')).toHaveLength(2)
    expect(w.find('[data-label="a"]').classes()).toContain('clash')
    expect(w.find('[data-label="c"]').classes()).not.toContain('clash')
  })

  it('marks Gmail and user labels differently', () => {
    const w = mount_()
    expect(w.find('[data-label="INBOX"]').text()).toContain('Gmail')
    expect(w.find('[data-label="a"]').text()).toContain('Yours')
  })

  it('emits an exclude toggle when a switch is turned off', async () => {
    const w = mount_()
    await w.find('[data-label="a"] input[type=checkbox]').setValue(false)
    expect(w.emitted('toggle-exclude')?.[0]).toEqual(['a', true])
  })

  it('offers folder editing for labels that copy, but not for excluded ones', async () => {
    const w = mount_()
    expect(w.find('[data-label="SPAM"] button').exists()).toBe(false)
    await w.find('[data-label="c"] button').trigger('click')
    expect(w.emitted('edit-folder')?.[0]?.[0]).toMatchObject({ id: 'c' })
  })
})
