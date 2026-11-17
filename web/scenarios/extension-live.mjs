import { chromium } from '@playwright/test'
import assert from 'node:assert/strict'
import http from 'node:http'
import { readFileSync, writeFileSync, mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { homedir, tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const admin = 'http://127.0.0.1:9998'
const token = readFileSync(join(homedir(), '.midopress/admin.token'), 'utf8').trim()
const out = resolve('../artifacts/extension-live')
mkdirSync(out, { recursive: true })
async function api(path, body) {
  const response = await fetch(admin + '/api/v1' + path, {
    method: body ? 'POST' : 'GET', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' },
    body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(10000),
  })
  assert.ok(response.ok, `administration ${path}: HTTP ${response.status}`)
  return response.json()
}
const before = await api('/stats')
const about = await api('/about')
assert.equal(about.sandbox.enforced, true, 'deployed service must verify kernel isolation')
const grant = await api('/images/credentials', { hosts: ['www.gstatic.com'], profiles: ['balanced'],
  max_width: 1024, max_dpr: 2, ttl_seconds: 300 })
const fixture = http.createServer((req, res) => {
  res.setHeader('Content-Type', 'text/html')
  res.end('<!doctype html><meta charset="utf-8"><title>MidoPress live extension</title><style>body{margin:40px;font:18px sans-serif;background:#f5f3ef}img{width:550px;height:auto;display:block}p{max-width:550px}</style><h1>Prueba pública de la extensión</h1><p>API real, credencial limitada y medición en Chrome.</p><img id="photo" src="https://www.gstatic.com/webp/gallery/1.jpg" alt="Imagen pública del corpus WebP de Google">')
})
await new Promise(resolve => fixture.listen(0, '127.0.0.1', resolve))
const profile = mkdtempSync(join(tmpdir(), 'midopress-live-extension-'))
let context
try {
  const extension = resolve('../extension')
  context = await chromium.launchPersistentContext(profile, { headless: true, channel: 'chromium',
    executablePath: process.env.MIDOPRESS_CHROME ?? chromium.executablePath(),
    args: ['--disable-extensions-except=' + extension, '--load-extension=' + extension],
    viewport: { width: 1280, height: 960 }, deviceScaleFactor: 1 })
  const worker = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker')
  await worker.evaluate(async config => chrome.storage.local.set({ config }), {
    endpoint: grant.public_url, token: grant.token, profile: 'balanced',
  })
  const page = await context.newPage()
  await page.goto('http://127.0.0.1:' + fixture.address().port)
  await page.locator('#photo').evaluate(image => image.decode())
  const id = await worker.evaluate(async url => (await chrome.tabs.query({})).find(t => t.url === url)?.id, page.url())
  assert.ok(id)
  await worker.evaluate(async tabId => chrome.scripting.executeScript({ target: { tabId }, files: ['measure.js', 'content.js'] }), id)
  await page.waitForFunction(() => document.querySelector('#photo').src.startsWith('data:image/'), null, { timeout: 60000 })
  const evidence = await worker.evaluate(async tabId => (await chrome.scripting.executeScript({ target: { tabId }, func: () => ({
    stats: { ...globalThis.__midoPress.stats }, width: document.querySelector('#photo').naturalWidth,
    painted: document.querySelector('#photo').getBoundingClientRect().width,
  }) }))[0].result, id)
  assert.equal(evidence.width, 550)
  assert.equal(evidence.painted, 550)
  assert.ok(evidence.stats.saved > 0)
  await page.screenshot({ path: join(out, 'public-image.png') })
  const dashboard = await context.newPage()
  await dashboard.goto(admin + '/#/login')
  await dashboard.getByLabel('Admin token').fill(token)
  await dashboard.getByRole('button', { name: 'Sign in' }).click()
  await dashboard.getByRole('heading', { name: 'Overview', exact: true }).waitFor()
  await dashboard.waitForTimeout(6000)
  await dashboard.screenshot({ path: join(out, 'dashboard.png'), fullPage: true })
  await dashboard.goto(admin + '/#/service')
  await dashboard.getByText('Codec permission isolation', { exact: true }).waitFor()
  await dashboard.screenshot({ path: join(out, 'service.png'), fullPage: true })
  const after = await api('/stats')
  assert.ok(after.totals.requests > before.totals.requests, 'continuous proxy traffic must remain active')
  const result = { updated: new Date().toISOString(), extension: evidence,
    sandbox: about.sandbox, jpeg_recompression_path: about.jpeg_recompression_path,
    continuous: { before: before.totals, after: after.totals },
    scope: { hosts: grant.scope.hosts, profiles: grant.scope.profiles, max_width: grant.scope.max_width,
      max_dpr: grant.scope.max_dpr, expires: grant.scope.expires } }
  writeFileSync(join(out, 'results.json'), JSON.stringify(result, null, 2))
  console.log(JSON.stringify({ passed: true, optimized: evidence.stats.optimized, saved: evidence.stats.saved,
    live_proxy_requests_added: after.totals.requests - before.totals.requests, sandbox: about.sandbox.policy }))
} finally {
  await context?.close()
  fixture.closeAllConnections()
  await new Promise(resolve => fixture.close(resolve))
  rmSync(profile, { recursive: true, force: true })
}
