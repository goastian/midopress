import { expect, test } from '@playwright/test'
import { TOKEN, ADMIN } from './env.mjs'

test('extension credentials are scoped, expiring and separate from the admin token', async ({ request }) => {
  const scope = { hosts: ['images.example.com'], profiles: ['balanced'], max_width: 600, max_dpr: 2, ttl_seconds: 60 }
  expect((await request.post(`${ADMIN}/api/v1/images/credentials`, { data: scope })).status()).toBe(401)
  const issued = await request.post(`${ADMIN}/api/v1/images/credentials`, { data: scope, headers: { Authorization: `Bearer ${TOKEN}` } })
  expect(issued.status()).toBe(201)
  const grant = await issued.json()
  expect(grant.token).toMatch(/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{43}$/)
  expect(grant.token).not.toBe(TOKEN)
  const endpoint = grant.public_url + '/img/sign'
  const data = { url: 'https://images.example.com/photo.png', profile: 'balanced', width: 300, dpr: 2, format: 'auto', version: '1' }
  const headers = { Authorization: `Bearer ${grant.token}` }
  const signed = await request.post(endpoint, { data, headers })
  expect(signed.status()).toBe(200)
  const image = await signed.json()
  expect(Date.parse(image.expires)).toBeLessThanOrEqual(grant.scope.expires * 1000)
  expect((await request.post(endpoint, { data, headers: { Authorization: `Bearer ${TOKEN}` } })).status()).toBe(403)
  expect((await request.post(endpoint, { data: { ...data, width: 601 }, headers })).status()).toBe(403)
  expect((await request.post(endpoint, { data: { ...data, url: 'https://other.example.com/photo.png' }, headers })).status()).toBe(403)
  expect((await request.post(endpoint, { data, headers: { ...headers, Cookie: 'session=secret' } })).status()).toBe(409)
  const forged = new URL(image.optimized_url)
  forged.searchParams.set('w', '301')
  expect((await request.get(forged.href)).status()).toBe(403)
})

test('quality and colour policies can be selected and persisted in the profile editor', async ({ page, request }) => {
  const headers = { Authorization: `Bearer ${TOKEN}` }
  await page.goto(`/#/login?token=${TOKEN}`)
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await page.goto('/#/rules')
  await page.getByRole('button', { name: 'Edit profile balanced', exact: true }).click()
  await page.getByLabel('Quality policy', { exact: true }).selectOption('pixels')
  await page.getByLabel('Colour policy', { exact: true }).selectOption('preserve')
  await page.getByRole('dialog', { name: 'Profile balanced', exact: true }).getByRole('button', { name: 'Save and apply', exact: true }).click()
  await expect.poll(async () => {
    const config = await (await request.get(`${ADMIN}/api/v1/config`, { headers })).json()
    return config.profiles.find((profile: { name: string }) => profile.name === 'balanced')?.quality_policy
  }).toBe('pixels')
  await page.reload()
  await page.getByRole('button', { name: 'Edit profile balanced', exact: true }).click()
  await expect(page.getByLabel('Quality policy', { exact: true })).toHaveValue('pixels')
  await expect(page.getByLabel('Colour policy', { exact: true })).toHaveValue('preserve')
  await request.delete(`${ADMIN}/api/v1/config/profiles/balanced`, { headers })
})
