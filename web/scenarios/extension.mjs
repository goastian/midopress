import { chromium } from '@playwright/test'
import http from 'node:http'
import { deflateSync } from 'node:zlib'
import { mkdirSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { tmpdir } from 'node:os'
import assert from 'node:assert/strict'

const output = resolve('../artifacts/extension')
mkdirSync(output, { recursive: true })
const requests = []
function crc(bytes) {
  let value = -1
  for (const byte of bytes) {
    value ^= byte
    for (let bit = 0; bit < 8; bit++) value = value >>> 1 ^ (value & 1 ? 0xedb88320 : 0)
  }
  return (value ^ -1) >>> 0
}
function chunk(type, data) {
  const body = Buffer.concat([Buffer.from(type), data])
  const size = Buffer.alloc(4); size.writeUInt32BE(data.length)
  const checksum = Buffer.alloc(4); checksum.writeUInt32BE(crc(body))
  return Buffer.concat([size, body, checksum])
}
function png(width, height) {
  const header = Buffer.alloc(13)
  header.writeUInt32BE(width); header.writeUInt32BE(height, 4); header[8] = 8; header[9] = 2
  const pixels = Buffer.alloc((width * 3 + 1) * height)
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) {
    const p = y * (width * 3 + 1) + 1 + x * 3
    pixels[p] = x % 256; pixels[p + 1] = y % 256; pixels[p + 2] = (x + y) % 256
  }
  return Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), chunk('IHDR', header), chunk('IDAT', deflateSync(pixels)), chunk('IEND', Buffer.alloc(0))])
}
const original = png(800, 400)
const token = Buffer.from(JSON.stringify({ hosts: ['127.0.0.1'], expires: Math.floor(Date.now() / 1000) + 600 })).toString('base64url') + '.' + 'A'.repeat(43)
const fixture = http.createServer(async (req, res) => {
  const u = new URL(req.url, 'http://' + req.headers.host)
  if (u.pathname === '/img/sign') {
    let body = ''
    for await (const data of req) body += data
    const input = JSON.parse(body)
    requests.push({ ...input, authorization: req.headers.authorization, cookie: req.headers.cookie })
    assert.equal(req.headers.authorization, 'Bearer ' + token)
    assert.equal(req.headers.cookie, undefined)
    if (input.url.includes('private')) { res.writeHead(403, { 'Content-Type': 'application/json' }).end('{"code":"outside_scope"}'); return }
    res.setHeader('Content-Type', 'application/json')
    res.end(JSON.stringify({ optimized_url: u.origin + '/img?w=' + input.width + '&dpr=' + input.dpr, expires: new Date(Date.now() + 30000).toISOString() }))
    return
  }
  if (u.pathname === '/img') {
    assert.equal(req.headers.authorization, undefined)
    assert.equal(req.headers.cookie, undefined)
    const width = Math.min(800, Math.ceil(Number(u.searchParams.get('w')) * Number(u.searchParams.get('dpr'))))
    if (u.searchParams.get('w') === '140') await new Promise(resolve => setTimeout(resolve, 200))
    const data = png(width, Math.max(1, Math.round(width / 2)))
    res.writeHead(200, { 'Content-Type': 'image/png', 'Content-Length': data.length,
      'X-MidoPress-Original-Bytes': original.length, 'X-MidoPress-Result-Bytes': data.length, 'X-MidoPress-Decision': 'optimized' })
    res.end(data)
    return
  }
  if (u.pathname.endsWith('.png')) { res.writeHead(200, { 'Content-Type': 'image/png' }).end(original); return }
  res.setHeader('Content-Type', 'text/html')
  res.end(`<!doctype html><style>body{margin:20px}.photo{width:180px;height:90px;object-fit:contain}.cover{width:100px;height:100px;object-fit:cover}.background{width:120px;height:70px;background-image:url('/background.png');background-size:cover}</style>
    <img id="photo" class="photo" src="/photo.png"><img id="cover" class="cover" src="/cover.png"><picture><source media="(max-width:600px)" srcset="/small.png"><img id="responsive" class="photo" src="/large.png"></picture><div id="background" class="background"></div><img id="private" class="photo" src="/private.png">`)
})
await new Promise(resolve => fixture.listen(0, '127.0.0.1', resolve))
const port = fixture.address().port
const profile = mkdtempSync(join(tmpdir(), 'midopress-extension-'))
const extension = resolve('../extension')
let context
let testPage
let testWorker
let testTab
const results = []
try {
  context = await chromium.launchPersistentContext(profile, { headless: true, channel: 'chromium',
    executablePath: process.env.MIDOPRESS_CHROME ?? chromium.executablePath(),
    args: ['--disable-extensions-except=' + extension, '--load-extension=' + extension],
    viewport: { width: 1000, height: 800 }, deviceScaleFactor: 2 })
  const worker = context.serviceWorkers()[0] ?? await context.waitForEvent('serviceworker')
  testWorker = worker
  await worker.evaluate(async config => { await chrome.storage.local.set({ config }) }, { endpoint: 'http://127.0.0.1:' + port, token, profile: 'balanced' })
  const page = await context.newPage()
  testPage = page
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto('http://127.0.0.1:' + port)
  const tabId = await worker.evaluate(async url => (await chrome.tabs.query({})).find(t => t.url === url)?.id, page.url())
  assert.ok(tabId)
  testTab = tabId
  const enable = () => worker.evaluate(async id => { await chrome.scripting.executeScript({ target: { tabId: id }, files: ['measure.js', 'content.js'] }) }, tabId)
  await enable()
  await page.waitForFunction(() => document.querySelector('#photo').src.startsWith('data:image/'), { timeout: 30000 })
  assert.ok(requests.some(r => r.url.endsWith('/photo.png') && r.width === 180 && r.dpr === 2))
  assert.ok(requests.some(r => r.url.endsWith('/cover.png') && r.width === 200))
  assert.equal(await page.locator('#private').getAttribute('src'), '/private.png')
  results.push('real extension loads, measures object-fit and DPR; credentials stay in background; denied image stays original')
  await page.evaluate(() => { document.querySelector('#photo').style.width = '240px'; document.querySelector('#photo').style.height = '120px' })
  await page.waitForFunction(() => document.querySelector('#photo').src.startsWith('data:image/') && document.querySelector('#photo').naturalWidth === 480)
  assert.ok(requests.some(r => r.url.endsWith('/photo.png') && r.width === 240))
  results.push('ResizeObserver follows JavaScript layout changes')
  await page.setViewportSize({ width: 500, height: 800 })
  await page.waitForFunction(() => document.querySelector('#responsive').src.startsWith('data:image/'))
  await page.waitForTimeout(500)
  assert.ok(requests.some(r => r.url.endsWith('/small.png')))
  results.push('picture media and srcset are reselected by the browser after viewport changes')
  await page.evaluate(() => { const image = new Image(); image.id = 'dynamic'; image.className = 'photo'; image.src = '/dynamic.png'; document.body.prepend(image) })
  await page.waitForFunction(() => document.querySelector('#dynamic').src.startsWith('data:image/'))
  results.push('MutationObserver handles dynamically inserted images')
  await page.waitForFunction(() => document.querySelector('#background').style.backgroundImage.includes('data:image/'))
  assert.ok(requests.some(r => r.url.endsWith('/background.png') && r.width >= 140))
  results.push('computed CSS background cover uses the real painted extent')
  await worker.evaluate(async id => { await chrome.scripting.executeScript({ target: { tabId: id }, func: () => globalThis.__midoPress.stop() }) }, tabId)
  assert.equal(await page.locator('#photo').getAttribute('src'), '/photo.png')
  assert.equal(await page.locator('source').getAttribute('srcset'), '/small.png')
  results.push('original URLs and responsive sources are restored')
  assert.deepEqual(errors, [])
  await page.screenshot({ path: join(output, 'restored.png') })
  writeFileSync(join(output, 'results.json'), JSON.stringify({ updated: new Date().toISOString(), kind: 'actual Chrome MV3 extension against a local synthetic signer/image fixture; real image API cryptographic and sandbox tests are separate', results,
    requests: requests.map(({ authorization, cookie, ...safe }) => safe) }, null, 2))
  console.log(JSON.stringify({ passed: results.length, results }))
} catch (error) {
  const diagnostic = await testWorker?.evaluate(async id => (await chrome.scripting.executeScript({ target: { tabId: id }, func: () => {
    const element = document.querySelector('#background')
    const bg = MidoPressMeasure.backgrounds(element)
    return { rect: element.getBoundingClientRect().toJSON(), css: getComputedStyle(element).backgroundImage.slice(0, 120),
      layers: bg.layers.map(x => ({ url: x.url.slice(0, 120), token: x.token.slice(0, 120) })), fit: bg.fit,
      measurement: MidoPressMeasure.backgroundSize(element, 800, 400, 0) }
  } }))[0].result, testTab).catch(() => null)
  writeFileSync(join(output, 'failure.json'), JSON.stringify({ error: error.message, diagnostic,
    passed: results, requests: requests.map(({ authorization, cookie, ...safe }) => safe) }, null, 2))
  await testPage?.screenshot({ path: join(output, 'failure.png') }).catch(() => {})
  throw error
} finally {
  await context?.close()
  fixture.closeAllConnections()
  await new Promise(resolve => fixture.close(resolve))
  rmSync(profile, { recursive: true, force: true })
}
