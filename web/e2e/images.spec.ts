import { expect, test } from '@playwright/test'
import { TOKEN } from './env.mjs'

const pixel = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jF1sAAAAASUVORK5CYII=', 'base64')

test('image comparison signs on the server, omits credentials and toggles versions', async ({ page }) => {
  const modes: string[] = []
  await page.route('http://127.0.0.1:29997/img?**', async (route) => {
    const req = route.request()
    const params = new URL(req.url()).searchParams
    expect(params.get('sig')).toMatch(/^[0-9a-f]{64}$/)
    expect(req.headers()['authorization']).toBeUndefined()
    expect(req.headers()['cookie']).toBeUndefined()
    modes.push(params.get('mode') ?? '')
    await route.fulfill({ status: 200, body: pixel, contentType: 'image/png', headers: {
      'Access-Control-Allow-Origin': '*', 'Access-Control-Expose-Headers': 'X-MidoPress-Cache, X-MidoPress-Decision, X-MidoPress-Backend',
      'X-MidoPress-Cache': 'miss', 'X-MidoPress-Decision': 'original', 'X-MidoPress-Backend': 'go',
    } })
  })
  await page.goto(`/#/login?token=${TOKEN}`)
  await page.getByRole('link', { name: 'Images', exact: true }).click()
  await page.getByLabel('Public image URL').fill('https://example.com/photo.png')
  await page.getByRole('button', { name: 'Compare image' }).click()
  await expect(page.getByRole('heading', { name: 'Comparison', exact: true })).toBeVisible()
  await expect(page.getByAltText('Optimized image')).toBeVisible()
  await expect(page.locator('main')).toContainText('Saved')
  await page.getByRole('button', { name: 'Original', exact: true }).click()
  await expect(page.getByAltText('Original image')).toBeVisible()
  await page.getByRole('button', { name: 'Optimized', exact: true }).click()
  await expect(page.getByAltText('Optimized image')).toBeVisible()
  expect(modes).toEqual(['original', 'optimized'])
})

test('private resources get an original fallback with a clear diagnostic', async ({ page }) => {
  await page.route('http://127.0.0.1:29997/img?**', (route) => route.fulfill({ status: 409, json: { code: 'session_resource', fallback: 'original' }, headers: { 'Access-Control-Allow-Origin': '*' } }))
  await page.goto(`/#/login?token=${TOKEN}`)
  await page.getByRole('link', { name: 'Images', exact: true }).click()
  await page.getByLabel('Public image URL').fill('https://example.com/private.png')
  await page.getByRole('button', { name: 'Compare image' }).click()
  await expect(page.getByRole('alert')).toContainText('session')
  await expect(page.getByRole('link', { name: 'Open original' })).toHaveAttribute('href', 'https://example.com/private.png')
})
