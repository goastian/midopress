import { expect, test, type Page } from '@playwright/test'
import http from 'node:http'
import { TOKEN, ADMIN } from './env.mjs'

const auth = { Authorization: `Bearer ${TOKEN}` }

async function signIn(page: Page) {
  await page.goto(`/#/login?token=${TOKEN}`)
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
}

// a page of text that the proxy compresses, through the proxy itself
function viaProxy(url: string): Promise<number> {
  return new Promise((resolve, reject) => {
    const req = http.request(
      { host: '127.0.0.1', port: 29999, path: url, headers: { Host: new URL(url).host, 'Accept-Encoding': 'gzip' } },
      (res) => {
        res.resume()
        res.on('end', () => resolve(res.statusCode ?? 0))
      },
    )
    req.on('error', reject)
    req.end()
  })
}

test.describe.configure({ mode: 'serial' })

test('the admin API refuses a request without the token', async ({ request }) => {
  expect((await request.get(`${ADMIN}/api/v1/stats`)).status()).toBe(401)
  expect((await request.get(`${ADMIN}/api/v1/stats`, { headers: auth })).status()).toBe(200)
})

test('a wrong token is refused on the sign in page', async ({ page }) => {
  await page.goto('/#/login')
  await page.getByLabel('Admin token').fill('not-the-token')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toContainText('not the admin token')
})

test('signing in with the link and seeing the traffic on the dashboard', async ({ page }) => {
  const origin = http.createServer((_, res) => {
    res.setHeader('Content-Type', 'text/html')
    res.end('<html><body>' + '<p>hello hello hello</p>'.repeat(400) + '</body></html>')
  })
  await new Promise<void>((r) => origin.listen(0, '127.0.0.1', r))
  const port = (origin.address() as { port: number }).port
  try {
    await signIn(page)
    for (let i = 0; i < 5; i++) expect(await viaProxy(`http://127.0.0.1:${port}/p${i}`)).toBe(200)
    await expect
      .poll(async () => (await (await page.request.get(`${ADMIN}/api/v1/stats`, { headers: auth })).json()).totals.requests)
      .toBeGreaterThanOrEqual(5)
    await expect(page.getByText('Throughput')).toBeVisible()
    await expect(page.getByText('By kind')).toBeVisible()
    await expect(page.locator('main')).toContainText('text')

    // the headline is what can be shrunk; video and what is never opened are apart
    await expect(page.getByText('of images, text and fonts')).toBeVisible()
    await expect(page.getByText('Video and audio')).toBeVisible()
    await expect(page.getByText('Not inspected')).toBeVisible()
    // and the bytes that were not saved have a reason and, with tracking on, a host
    await expect(page.getByRole('heading', { name: 'Left alone' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Causes' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Where bytes were not saved' })).toBeVisible()
    await expect(page.locator('main')).not.toContainText('unknown')
  } finally {
    origin.close()
  }
})

test('changing the default profile in the interface applies without a restart', async ({ page }) => {
  await signIn(page)
  await page.goto('/#/rules')
  const select = page.locator('select').first()
  await expect(select).toBeVisible()
  await select.selectOption('saver')
  await expect
    .poll(async () => (await (await page.request.get(`${ADMIN}/api/v1/config`, { headers: auth })).json()).default_profile)
    .toBe('saver')
  await page.reload()
  await expect(page.locator('select').first()).toHaveValue('saver')
  const view = await (await page.request.delete(`${ADMIN}/api/v1/config/ui`, { headers: auth })).json()
  expect(view.default_profile).toBe('balanced')
})

test('a rule with a syntax error is marked and cannot be saved', async ({ page }) => {
  await signIn(page)
  await page.goto('/#/rules')
  await page.getByRole('button', { name: /add rule/i }).click()
  await page.locator('input.mono').first().fill('~d (')
  await expect(page.getByRole('button', { name: /^save/i }).first()).toBeDisabled()
})

test('the connect page shows the PAC address, offers the CA and the magic host serves it', async ({ page, request }) => {
  await signIn(page)
  await page.goto('/#/certificate')
  await expect(page).toHaveURL(/#\/connect$/)
  await expect(page.getByRole('heading', { name: 'Connect', exact: true })).toBeVisible()
  await expect(page.locator('#pac-url')).toHaveValue(/^http:\/\/[^/]+:29999\/proxy\.pac$/)
  await expect(page.locator('#full-url')).toHaveValue(/:29999\/proxy-full\.pac$/)
  const ca = await (await request.get(`${ADMIN}/api/v1/ca`, { headers: auth })).json()
  expect(ca.available).toBe(true)
  const groups = ca.sha256.match(/../g).join(' ')
  await expect(page.locator('main')).toContainText(groups.slice(0, 11))
  await expect(page.getByRole('button', { name: /PEM/ })).toBeVisible()

  const pem = await request.get(`${ADMIN}/api/v1/ca.pem`, { headers: auth })
  expect(await pem.text()).toContain('BEGIN CERTIFICATE')
  expect(await pem.text()).not.toContain('PRIVATE KEY')

  const status = await new Promise<number>((resolve, reject) => {
    const r = http.request({ host: '127.0.0.1', port: 29999, path: 'http://midopress.it/', headers: { Host: 'midopress.it' } }, (res) => {
      res.resume()
      res.on('end', () => resolve(res.statusCode ?? 0))
    })
    r.on('error', reject)
    r.end()
  })
  expect(status).toBe(200)
})

test('the live traffic list is off until asked for', async ({ page }) => {
  await signIn(page)
  await page.goto('/#/traffic')
  await expect(page.locator('main')).toContainText(/off|turn on|memory/i)
  const s = await (await page.request.get(`${ADMIN}/api/v1/flows`, { headers: auth })).json()
  expect(s.enabled).toBe(false)
})

test('every page opens without errors', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await signIn(page)
  for (const p of ['/', '/traffic', '/devices', '/rules', '/connect', '/logs', '/service']) {
    await page.goto(`/#${p}`)
    await expect(page.locator('main h1').first()).toBeVisible()
  }
  expect(errors).toEqual([])
})
