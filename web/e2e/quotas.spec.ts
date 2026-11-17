import { expect, test, type Page } from '@playwright/test'
import { TOKEN } from './env.mjs'
import type { Device, DeviceQuotaLimits } from '../src/api/types'

const globalLimits: DeviceQuotaLimits = { requests: 100000, bytes: 10737418240, concurrency: 16 }

async function devicesPage(page: Page) {
  let device: Device = {
    id: 'quota-phone', profile: 'saver', created: '2026-11-17T00:00:00Z', last_seen: null,
    active: false, requests: 100, bytes_in: 4096, bytes_out: 2048, saved: 2048,
    quota: { requests: 42, bytes: 1024, active: 0, tunnels: 2, window_start: '2026-11-17T00:00:00Z',
      window_end: '2026-11-18T00:00:00Z', limits: globalLimits },
  }
  const patches: { quotas?: DeviceQuotaLimits | null; profile?: string }[] = []
  let refuse = false
  await page.route('**/api/v1/devices', route => route.fulfill({ json: [device] }))
  await page.route('**/api/v1/devices/quota-phone', async route => {
    expect(route.request().method()).toBe('PATCH')
    const body = route.request().postDataJSON() as typeof patches[number]
    patches.push(body)
    if (refuse) {
      refuse = false
      await route.fulfill({ status: 422, json: { code: 'invalid_quota', error: 'The server refused these limits.' } })
      return
    }
    if ('quotas' in body) {
      if (body.quotas === null) delete device.quotas
      else device.quotas = body.quotas
      device.quota = { ...device.quota!, limits: body.quotas ?? globalLimits }
    }
    if (body.profile !== undefined) device.profile = body.profile
    await route.fulfill({ json: device })
  })
  await page.goto(`/#/login?token=${TOKEN}`)
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await page.goto('/#/devices')
  await expect(page.getByRole('button', { name: 'Quotas of quota-phone' })).toBeVisible()
  return { patches, refuseNext: () => { refuse = true } }
}

test('device limits save and reset inheritance without changing its profile or charged usage', async ({ page }) => {
  const state = await devicesPage(page)
  await page.getByRole('button', { name: 'Quotas of quota-phone' }).click()
  const dialog = page.getByRole('dialog', { name: 'Quotas for quota-phone' })
  await expect(dialog).toContainText('Current window, in your local time:')
  await expect(dialog).toContainText('independent concurrency pools')
  await expect(dialog.locator('dt').filter({ hasText: /^Active requests$/ }).locator('xpath=following-sibling::dd[1]')).toHaveText('0 / 16')
  await expect(dialog.getByLabel('Use global limits')).toBeChecked()
  await dialog.getByLabel('Use global limits').uncheck()
  await dialog.getByLabel('Request limit per window').fill('500')
  await dialog.getByLabel('Byte limit per window').fill('1048576')
  await dialog.getByLabel('Concurrency limit per pool').fill('3')
  await dialog.getByRole('button', { name: 'Save limits' }).click()
  await expect(dialog).toHaveCount(0)
  expect(state.patches).toEqual([{ quotas: { requests: 500, bytes: 1048576, concurrency: 3 } }])
  await expect(page.getByLabel('Profile of quota-phone')).toHaveValue('saver')
  await page.getByRole('button', { name: 'Quotas of quota-phone' }).click()
  await expect(dialog.getByLabel('Request limit per window')).toHaveValue('500')
  await expect(dialog).toContainText('42 / 500')
  await dialog.getByLabel('Use global limits').check()
  await dialog.getByRole('button', { name: 'Save limits' }).click()
  await expect(dialog).toHaveCount(0)
  expect(state.patches[1]).toEqual({ quotas: null })
  await expect(page.getByLabel('Profile of quota-phone')).toHaveValue('saver')
})

test('zero limits remain explicit unlimited values; invalid limits send no request', async ({ page }) => {
  const state = await devicesPage(page)
  await page.getByRole('button', { name: 'Quotas of quota-phone' }).click()
  const dialog = page.getByRole('dialog', { name: 'Quotas for quota-phone' })
  await dialog.getByLabel('Use global limits').uncheck()
  await dialog.getByLabel('Request limit per window').fill('-1')
  await expect(dialog.getByRole('button', { name: 'Save limits' })).toBeDisabled()
  await dialog.getByLabel('Request limit per window').fill('0.5')
  await expect(dialog.getByRole('button', { name: 'Save limits' })).toBeDisabled()
  await dialog.getByLabel('Request limit per window').fill('0')
  await dialog.getByLabel('Byte limit per window').fill('0')
  await dialog.getByLabel('Concurrency limit per pool').fill('10001')
  await expect(dialog.getByRole('button', { name: 'Save limits' })).toBeDisabled()
  expect(state.patches).toEqual([])
  await dialog.getByLabel('Concurrency limit per pool').fill('0')
  await dialog.getByRole('button', { name: 'Save limits' }).click()
  await expect(dialog).toHaveCount(0)
  expect(state.patches).toEqual([{ quotas: { requests: 0, bytes: 0, concurrency: 0 } }])
  await page.getByRole('button', { name: 'Quotas of quota-phone' }).click()
  await expect(dialog.getByLabel('Use global limits')).not.toBeChecked()
  await expect(dialog).toContainText('42 / unlimited')
})

test('a rejected quota change keeps the editor available for correction', async ({ page }) => {
  const state = await devicesPage(page)
  await page.getByRole('button', { name: 'Quotas of quota-phone' }).click()
  const dialog = page.getByRole('dialog', { name: 'Quotas for quota-phone' })
  await dialog.getByLabel('Use global limits').uncheck()
  await dialog.getByLabel('Request limit per window').fill('600')
  state.refuseNext()
  await dialog.getByRole('button', { name: 'Save limits' }).click()
  await expect(dialog.getByRole('alert')).toHaveText('The server refused these limits.')
  await expect(dialog.getByLabel('Request limit per window')).toHaveValue('600')
  await dialog.getByLabel('Request limit per window').fill('700')
  await dialog.getByRole('button', { name: 'Save limits' }).click()
  await expect(dialog).toHaveCount(0)
  expect(state.patches[1]?.quotas?.requests).toBe(700)
})
