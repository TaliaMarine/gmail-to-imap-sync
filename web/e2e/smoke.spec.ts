import { expect, test, type Page } from '@playwright/test'

// Unique per run so reruns against the same database do not collide.
const run = Date.now().toString(36)

/** Collects console errors and failed API calls so each test can assert none happened. */
function watch(page: Page) {
  const problems: string[] = []
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`))
  page.on('console', (m) => m.type() === 'error' && problems.push(`console: ${m.text()}`))
  page.on('response', (r) => r.url().includes('/api/') && r.status() >= 500 && problems.push(`http ${r.status()} ${r.url()}`))
  return problems
}

test('every page in the navigation loads without errors', async ({ page }) => {
  const problems = watch(page)
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  for (const name of ['Accounts', 'Destinations', 'Sync', 'History', 'Failures', 'System']) {
    await page.getByRole('navigation', { name: 'Main navigation' }).getByRole('link', { name }).click()
    await expect(page.getByRole('heading', { level: 1, name })).toBeVisible()
  }
  expect(problems).toEqual([])
})

test('the Google setup steps are offered until the client is configured', async ({ page }) => {
  await page.goto('/overview')
  const wizard = page.getByTestId('setup-wizard')
  test.skip(!(await wizard.isVisible()), 'The Google client is already configured in this database')
  await expect(wizard.getByText('In production')).toBeVisible()
  await expect(wizard.getByRole('textbox', { name: 'Redirect URI' })).toHaveValue(/\/oauth\/callback$/)
})

test('add a destination, then an account that uses it', async ({ page }) => {
  const problems = watch(page)
  await page.goto('/destinations')
  await page.getByRole('button', { name: 'Add destination' }).first().click()
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(`Dovecot ${run}`)
  await page.getByRole('textbox', { name: 'Server' }).fill('dovecot.example.com')
  await page.getByRole('textbox', { name: 'Username' }).fill('archive')
  await page.getByLabel('Password', { exact: true }).fill('s3cret')
  await page.getByRole('button', { name: 'Add destination' }).last().click()
  await expect(page.getByText(`Dovecot ${run}`)).toBeVisible()
  await expect(page.getByText('s3cret')).toHaveCount(0) // write-only

  await page.goto('/accounts?new=1')
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(`Account ${run}`)
  await page.locator('.v-dialog .v-select .v-field').click()
  await page.getByRole('option', { name: `Dovecot ${run}` }).click()
  await page.getByRole('button', { name: 'Add account' }).last().click()
  await expect(page.getByRole('heading', { name: `Account ${run}` })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Authorise with Google' })).toBeVisible()
  expect(problems).toEqual([])
})

test('an OAuth error from Google is explained in plain language', async ({ page }) => {
  await page.goto('/accounts')
  await page.getByRole('link', { name: `Account ${run}` }).click()
  await page.waitForURL(/\/accounts\/[0-9a-f-]{36}/)
  const url = new URL(page.url())
  await page.goto(`${url.pathname}?oauth_error=no_refresh_token`)
  await expect(page.getByTestId('oauth-error')).toContainText('long-lived token')
})

test('schedule changes show what will change before saving', async ({ page }) => {
  await page.goto('/accounts')
  await page.getByRole('link', { name: `Account ${run}` }).click()
  await page.waitForURL(/\/accounts\/[0-9a-f-]{36}/)
  await page.getByRole('tab', { name: 'Schedule' }).click()
  await page.getByLabel('Sync on a schedule').check()
  await page.getByRole('button', { name: 'Review and save' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('Scheduled sync is turned on')
  await expect(dialog).toContainText('nothing already copied is moved or deleted')
  await dialog.getByRole('button', { name: 'Save changes' }).click()
  await expect(page.getByText('Saved settings')).toBeVisible()
})

test('history and failures show inviting empty states with filters in the URL', async ({ page }) => {
  await page.goto('/history?status=cancelled')
  await expect(page.getByText(/No sessions/)).toBeVisible()
  await page.goto('/failures?category=parse')
  await expect(page.getByText(/No failures match|No failed messages/)).toBeVisible()
})
