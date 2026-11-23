import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { Failure } from '../services/client'
import FailureTable from './FailureTable.vue'

const f = (id: string, o: Partial<Failure> = {}): Failure => ({
  id, account_id: 'a', attempt_count: 1, auto_retry: true, category: 'imap_append', completed_folders: ['INBOX'], description: 'The IMAP server refused it.',
  error_message: 'x', failed_folders: ['Finance'], first_failed_at: '2026-01-01T00:00:00Z', gmail_message_id: id, gmail_thread_id: 't',
  intended_folders: [], last_attempted_at: '2026-01-01T00:00:00Z', last_failed_at: '2026-01-01T00:00:00Z', retry_exhausted: false,
  sender: 's@x.com', status: 'pending', subject: `Subject ${id}`, ...o,
})
const items = [f('1'), f('2'), f('3', { subject: '', status: 'ignored' })]

describe('FailureTable', () => {
  it('lists failures with problem, folder state and status', () => {
    const w = mount(FailureTable, { props: { items, selected: [] } })
    expect(w.findAll('[data-testid=failure-row]')).toHaveLength(3)
    expect(w.text()).toContain('IMAP delivery')
    expect(w.text()).toContain('✓ INBOX')
    expect(w.text()).toContain('✗ Finance')
    expect(w.text()).toContain('(no subject)')
    expect(w.text()).toContain('Ignored')
  })

  it('selects one row', async () => {
    const w = mount(FailureTable, { props: { items, selected: [] } })
    await w.findAll('tbody input[type=checkbox]')[1]!.setValue(true)
    expect(w.emitted('update:selected')?.[0]).toEqual([['2']])
  })

  it('adds to and removes from an existing selection', async () => {
    const w = mount(FailureTable, { props: { items, selected: ['1'] } })
    await w.findAll('tbody input[type=checkbox]')[1]!.setValue(true)
    expect(w.emitted('update:selected')?.[0]).toEqual([['1', '2']])
    await w.findAll('tbody input[type=checkbox]')[0]!.setValue(false)
    expect(w.emitted('update:selected')?.[1]).toEqual([[]])
  })

  it('selects and clears every row on the page from the header', async () => {
    const w = mount(FailureTable, { props: { items, selected: [] } })
    await w.find('[data-testid=select-all]').find('input').setValue(true)
    expect(w.emitted('update:selected')?.[0]).toEqual([['1', '2', '3']])
    await w.setProps({ selected: ['1', '2', '3'] })
    await w.find('[data-testid=select-all]').find('input').setValue(false)
    expect(w.emitted('update:selected')?.[1]).toEqual([[]])
  })

  it('keeps selections made on other pages when the header is cleared', async () => {
    const w = mount(FailureTable, { props: { items, selected: ['1', '2', '3', 'elsewhere'] } })
    await w.find('[data-testid=select-all]').find('input').setValue(false)
    expect(w.emitted('update:selected')?.[0]).toEqual([['elsewhere']])
  })

  it('opens the details when the subject is chosen', async () => {
    const w = mount(FailureTable, { props: { items, selected: [] } })
    await w.find('button.link').trigger('click')
    expect(w.emitted('open')?.[0]?.[0]).toMatchObject({ id: '1' })
  })
})
