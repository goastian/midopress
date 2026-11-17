import { chromium } from '@playwright/test'
import { mkdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { cpus, totalmem, release, platform } from 'node:os'
import { createHash } from 'node:crypto'
import { startServer, settle, sleep, metric } from './lib.mjs'

const arg = (name, fallback) => {
  const i = process.argv.indexOf('--' + name)
  return i < 0 ? fallback : process.argv[i + 1]
}
const catalog = [
  { id: 'control', url: 'https://www.gstatic.com/webp/gallery/1.png', control: true },
  { id: 'youtube', url: 'https://www.youtube.com/watch?v=aqz-KE-bpKQ', video: true },
  { id: 'amazon', url: 'https://www.amazon.com/s?k=wireless+headphones' },
  { id: 'alibaba', url: 'https://www.alibaba.com/' },
  { id: 'aliexpress', url: 'https://www.aliexpress.com/' },
  { id: 'google', url: 'https://www.google.com/search?q=software+libre' },
  { id: 'facebook', url: 'https://www.facebook.com/' },
  { id: 'pinterest', url: 'https://www.pinterest.com/search/pins/?q=architecture' },
  { id: 'wikipedia', url: 'https://en.wikipedia.org/wiki/Main_Page' },
  { id: 'github', url: 'https://github.com/golang/go' },
  { id: 'bbc', url: 'https://www.bbc.com/news' },
  { id: 'cnn', url: 'https://www.cnn.com/' },
  { id: 'reddit', url: 'https://www.reddit.com/r/technology/' },
  { id: 'mercadolibre', url: 'https://listado.mercadolibre.com.co/audifonos' },
  { id: 'stackoverflow', url: 'https://stackoverflow.com/questions' },
  { id: 'duckduckgo', url: 'https://duckduckgo.com/?q=software+libre' },
  { id: 'eltiempo', url: 'https://www.eltiempo.com/' },
]
const selected = arg('sites', catalog.filter(s => !s.control).map(s => s.id).join(',')).split(',')
const sites = selected.map(id => {
  const site = catalog.find(s => s.id === id)
  if (!site) throw new Error('unknown site: ' + id)
  return site
})
const profiles = arg('profiles', 'balanced,saver,extreme,lossless').split(',')
const bin = resolve(arg('bin', '../bin/midopress'))
const out = resolve(arg('out', '../artifacts/popular-baseline'))
const existing = process.argv.includes('--existing')
const directOnly = process.argv.includes('--direct-only')
const skipDirect = process.argv.includes('--skip-direct')
const nativeLibs = arg('native-libs', '/home/ponchale/.midopress/native')
const network = arg('network', 'native')
const browserPath = arg('chromium', undefined)
const staticWait = arg('static-wait-ms', undefined)
const serverConfig = staticWait === undefined ? undefined : join(out, 'profiles.yaml')
const navigationTimeout = Number(arg('timeout-ms', '25000'))
const dwell = Number(arg('dwell-ms', '4000'))
const scrolls = Number(arg('scrolls', '3'))
const maxBytes = Number(arg('max-bytes', String(120 * 1024 * 1024)))
mkdirSync(join(out, 'screenshots'), { recursive: true })
if (serverConfig) writeFileSync(serverConfig, 'profiles:\n' + ['balanced', 'saver', 'extreme', 'lossless'].map(p => `  ${p}:\n    text: {static_wait_ms: ${Number(staticWait)}}\n`).join(''))
const reportFile = join(out, 'results.json')
const report = process.argv.includes('--append') && existsSync(reportFile)
  ? JSON.parse(readFileSync(reportFile, 'utf8'))
  : { started: new Date().toISOString(), methodology: {
      browserCache: 'disabled; fresh browser/context for every visit', serviceWorkers: 'blocked',
      network, navigationTimeout, dwell, scrolls, maxBytes, viewport: { width: 1365, height: 850 },
      tls: 'isolated test browsers ignore certificate errors; proxy verifies origin TLS',
      quic: 'disabled equally in direct/proxy runs',
      inp: 'not measured; scroll-only navigation is not a field INP measurement',
      warning: 'Live pages vary. Proxy byte deltas are within-response savings; direct/proxy browser totals may cover different content.',
    }, machine: { os: platform(), release: release(), cpu: cpus()[0]?.model, logicalCPUs: cpus().length, ramBytes: totalmem() }, runs: [] }
let stopping = false
let activeBrowser
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => { stopping = true; void activeBrowser?.close() })
const checkpoint = () => writeFileSync(reportFile, JSON.stringify(report, null, 2) + '\n')
if (!existing && process.argv.includes('--append')) {
  for (const site of sites) for (const profile of profiles) {
    const phases = report.runs.filter(r => r.site === site.id && r.profile === profile).map(r => r.phase)
    if (phases.includes('cold') !== phases.includes('warm')) {
      report.runs = report.runs.filter(r => r.site !== site.id || r.profile !== profile)
    }
  }
}
if (!existing) report.binarySHA256 = createHash('sha256').update(readFileSync(bin)).digest('hex')
const have = (site, profile, phase) => report.runs.some(r => r.site === site && r.profile === profile && r.phase === phase)

function counterDelta(before, after) {
  const bucket = (a = {}, b = {}) => Object.fromEntries(['requests', 'transformed', 'bytes_in', 'bytes_out', 'saved'].map(k => [k, k === 'saved' && b[k] === undefined ? null : (b[k] ?? 0) - (a[k] ?? 0)]))
  const kinds = Object.fromEntries(['image', 'text', 'font', 'media', 'other'].map(k => [k, bucket(before.by_kind?.[k], after.by_kind?.[k])]))
  const totals = bucket(before.totals, after.totals)
  const byReason = (after.by_reason ?? []).map(row => {
    const prev = before.by_reason?.find(r => r.reason === row.reason && r.kind === row.kind)
    return { reason: row.reason, kind: row.kind, ...bucket(prev, row) }
  }).filter(row => row.requests > 0)
  const byCause = (after.by_cause ?? []).map(row => {
    const prev = before.by_cause?.find(r => r.reason === row.reason && r.cause === row.cause)
    return { reason: row.reason, cause: row.cause, requests: row.requests - (prev?.requests ?? 0), bytes_in: row.bytes_in - (prev?.bytes_in ?? 0) }
  }).filter(row => row.requests > 0)
  const compressed = ['image', 'text', 'font'].reduce((sum, k) => ({ bytes_in: sum.bytes_in + kinds[k].bytes_in, bytes_out: sum.bytes_out + kinds[k].bytes_out }), { bytes_in: 0, bytes_out: 0 })
  const saved = (kind) => kinds[kind].saved ?? byReason.filter(r => r.kind === kind && r.reason === 'transformed').reduce((sum, r) => sum + r.bytes_in - r.bytes_out, 0)
  compressed.saved = ['image', 'text', 'font'].reduce((sum, k) => sum + saved(k), 0)
  totals.saved ??= Object.keys(kinds).reduce((sum, k) => sum + saved(k), 0)
  const tunnel = { up: (after.connections?.tunnel_bytes_up ?? 0) - (before.connections?.tunnel_bytes_up ?? 0), down: (after.connections?.tunnel_bytes_down ?? 0) - (before.connections?.tunnel_bytes_down ?? 0) }
  return { totals, kinds, compressed, byReason, byCause, tunnel,
    savingsPercent: compressed.bytes_in ? compressed.saved / compressed.bytes_in * 100 : null,
    totalSavingsPercent: totals.bytes_in ? totals.saved / totals.bytes_in * 100 : null,
    cacheHits: (after.cache?.hits ?? 0) - (before.cache?.hits ?? 0),
    cacheMisses: (after.cache?.misses ?? 0) - (before.cache?.misses ?? 0),
    pipelineLatency: after.latency, cpu: after.cpu, memory: after.memory,
    tlsHandshakeErrors: (after.connections?.tls_handshake_errors ?? 0) - (before.connections?.tls_handshake_errors ?? 0) }
}

async function measure(site, proxyURL, profile, phase, caFile) {
  const row = { site: site.id, url: site.url, profile, phase, started: new Date().toISOString() }
  const browser = await chromium.launch({ headless: true,
    ...(browserPath ? { executablePath: browserPath } : {}),
    args: ['--disable-quic', '--ignore-certificate-errors', '--autoplay-policy=no-user-gesture-required'],
    ...(proxyURL ? { proxy: { server: proxyURL, bypass: '<-loopback>' } } : {}),
  })
  activeBrowser = browser
  const watchdog = setTimeout(() => { row.deadline = true; void browser.close() }, navigationTimeout + dwell + 25000)
  const resources = new Map()
  const redirects = []
  const failures = []
  let transferred = 0
  let budgetBytes = 0
  let pageErrors = 0
  try {
    const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: report.methodology.viewport, locale: 'es-CO', serviceWorkers: 'block' })
    await context.addInitScript(() => {
      const p = window.__bench = { lcp: null, cls: 0, session: 0, first: 0, last: 0 }
      try { new PerformanceObserver(list => { for (const e of list.getEntries()) p.lcp = e.renderTime || e.loadTime || e.startTime }).observe({ type: 'largest-contentful-paint', buffered: true }) } catch {}
      try { new PerformanceObserver(list => {
        for (const e of list.getEntries()) {
          if (e.hadRecentInput) continue
          if (e.startTime - p.last < 1000 && e.startTime - p.first < 5000) p.session += e.value
          else { p.session = e.value; p.first = e.startTime }
          p.last = e.startTime
          p.cls = Math.max(p.cls, p.session)
        }
      }).observe({ type: 'layout-shift', buffered: true }) } catch {}
    })
    const page = await context.newPage()
    page.on('pageerror', () => pageErrors++)
    const cdp = await context.newCDPSession(page)
    await cdp.send('Network.enable')
    await cdp.send('Network.setCacheDisabled', { cacheDisabled: true })
    await cdp.send('Network.setBypassServiceWorker', { bypass: true })
    if (network === '5mbps') await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 40, downloadThroughput: 5e6 / 8, uploadThroughput: 1e6 / 8 })
    cdp.on('Network.requestWillBeSent', event => {
      if (!/^https?:/.test(event.request.url)) return
      if (event.redirectResponse) redirects.push({ host: new URL(event.redirectResponse.url).hostname, type: event.type,
        status: event.redirectResponse.status, bytes: event.redirectResponse.encodedDataLength ?? 0, finished: true, redirect: true })
      const u = new URL(event.request.url)
      resources.set(event.requestId, { host: u.hostname, type: event.type, started: event.timestamp, bytes: 0 })
    })
    cdp.on('Network.responseReceived', event => {
      const resource = resources.get(event.requestId)
      if (!resource) return
      const headers = Object.fromEntries(Object.entries(event.response.headers).map(([k, v]) => [k.toLowerCase(), String(v)]))
      Object.assign(resource, { status: event.response.status, mime: event.response.mimeType, protocol: event.response.protocol,
        issuer: event.response.securityDetails?.issuer,
        cached: event.response.fromDiskCache || event.response.fromServiceWorker || false,
        encoding: headers['content-encoding'] ?? '', cacheControl: headers['cache-control'] ?? '',
        certificateIssuer: event.response.securityDetails?.issuer })
    })
    cdp.on('Network.dataReceived', event => {
      transferred += event.encodedDataLength
      budgetBytes += Math.max(event.dataLength, event.encodedDataLength)
      const resource = resources.get(event.requestId)
      if (resource && !resource.finished) resource.bytes += event.encodedDataLength
      if (budgetBytes > maxBytes && !row.byteLimit) { row.byteLimit = true; void browser.close() }
    })
    cdp.on('Network.loadingFinished', event => {
      const resource = resources.get(event.requestId)
      if (resource) Object.assign(resource, { bytes: event.encodedDataLength, ms: (event.timestamp - resource.started) * 1000, finished: true })
    })
    cdp.on('Network.loadingFailed', event => {
      failures.push({ type: event.type, error: event.errorText, canceled: !!event.canceled })
    })
    const start = performance.now()
    try {
      const response = await page.goto(site.url, { waitUntil: 'domcontentloaded', timeout: navigationTimeout })
      row.status = response?.status()
    } catch (error) { row.navigationError = String(error.message).split('\n')[0].replace(/https?:\/\/\S+/g, '[url]') }
    await sleep(dwell)
    if (site.video) {
      await page.evaluate(() => { const video = document.querySelector('video'); if (video) { video.muted = true; void video.play().catch(() => {}) } }).catch(() => {})
      await sleep(12000)
    }
    row.firstViewport = await page.evaluate(() => {
      const nav = performance.getEntriesByType('navigation')[0]
      const b = window.__bench ?? {}
      return { ttfb_ms: nav?.responseStart || null, origin_wait_ms: nav ? nav.responseStart - nav.requestStart : null,
        dcl_ms: nav?.domContentLoadedEventEnd || null, load_ms: nav?.loadEventEnd || null,
        fcp_ms: performance.getEntriesByName('first-contentful-paint')[0]?.startTime ?? null,
        lcp_ms: b.lcp ?? null, cls: b.cls ?? null, lcp_censored: !nav?.loadEventEnd,
        protocol: nav?.nextHopProtocol, renderBlockingScripts: document.querySelectorAll('script[src]:not([async]):not([defer]):not([type="module"])').length }
    }).catch(() => null)
    for (let i = 0; i < scrolls; i++) { await page.mouse.wheel(0, 650); await sleep(700) }
    row.page = await page.evaluate(() => {
      const text = document.body?.innerText?.slice(0, 20000) ?? ''
      const videos = [...document.querySelectorAll('video')].map(v => ({ time: v.currentTime, paused: v.paused, width: v.videoWidth, height: v.videoHeight, readyState: v.readyState }))
      return { title: document.title, host: location.hostname, textLength: text.length,
        loadedImages: [...document.images].filter(i => i.complete && i.naturalWidth > 0).length,
        brokenImages: [...document.images].filter(i => i.currentSrc && i.complete && i.naturalWidth === 0).length,
        passwordInputs: document.querySelectorAll('input[type="password"]').length,
        challenge: /verify you are human|unusual traffic|automated access|robot check|captcha|access denied|checking your browser|blocked by network security|verifica que eres|tráfico inusual/i.test(text + ' ' + document.title),
        videos }
    }).catch(() => null)
    row.elapsed_ms = performance.now() - start
    row.outcome = !row.status ? 'navigation_error' : row.status >= 400 ? 'http_error' : row.page?.challenge ? 'challenge'
      : site.id === 'facebook' && row.page?.passwordInputs ? 'login_wall'
      : !row.page || row.page.textLength < 200 && row.page.loadedImages < 2 ? 'empty' : 'loaded'
    if (site.control && row.status === 200 && row.page?.loadedImages > 0) row.outcome = 'loaded'
    row.valid = row.outcome === 'loaded' && (!site.video || row.page.videos.some(v => v.time > 1))
    if (site.video && row.outcome === 'loaded' && !row.valid) row.outcome = 'video_unavailable'
    await page.screenshot({ path: join(out, 'screenshots', `${site.id}-${profile}-${phase}.png`), timeout: 5000 }).catch(() => {})
    row.pageErrors = pageErrors
  } catch (error) {
    row.error = String(error.message).split('\n')[0].replace(/https?:\/\/\S+/g, '[url]')
    row.outcome = row.byteLimit ? 'byte_limit' : row.deadline ? 'deadline' : 'error'
    row.valid = false
  } finally {
    clearTimeout(watchdog)
    row.resources = [...resources.values(), ...redirects].map(({ started, ...rest }) => rest)
    row.browserBytes = row.resources.reduce((sum, resource) => sum + resource.bytes, 0)
    row.streamBytes = transferred
    row.budgetBytes = budgetBytes
    row.requests = resources.size + redirects.length
    row.failures = failures
    await browser.close().catch(() => {})
    activeBrowser = null
  }
  return row
}

async function runVisit(site, service, profile, phase, extra = {}) {
  if (stopping || have(site.id, profile, phase)) return
  const before = service ? await service.stats() : null
  const noTransformBefore = service?.metrics ? metric(await service.metrics(), 'midopress_pipeline_no_transform_overridden_total', { kind: 'image' }) : 0
  process.stdout.write(`${site.id} ${profile} ${phase} ... `)
  const row = await measure(site, service?.proxyURL, profile, phase, service?.caFile ?? (service?.dir ? join(service.dir, 'ca.crt') : undefined))
  if (service) {
    await sleep(500)
    try {
      const after = await service.stats()
      const boot = s => Date.parse(s.time) - s.uptime_seconds * 1000
      if (Math.abs(boot(after) - boot(before)) > 1000 || after.totals.requests < before.totals.requests) {
        row.pageOutcome = row.outcome; row.outcome = 'counter_reset'; row.valid = false
      } else {
        row.proxy = counterDelta(before, after)
        row.proxy.intercepted = row.proxy.totals.requests > 0
        row.proxy.noTransformImages = service.metrics ? metric(await service.metrics(), 'midopress_pipeline_no_transform_overridden_total', { kind: 'image' }) - noTransformBefore : null
        if (!row.proxy.intercepted && row.valid) { row.pageOutcome = row.outcome; row.outcome = 'tls_tunnel'; row.valid = false }
      }
    } catch {
      row.pageOutcome = row.outcome; row.outcome = 'server_unavailable'; row.valid = false
    }
  }
  Object.assign(row, extra)
  report.runs.push(row)
  checkpoint()
  process.stdout.write(`${row.outcome}; ${row.browserBytes} B; LCP ${Math.round(row.firstViewport?.lcp_ms ?? 0)} ms${row.proxy ? `; saves ${row.proxy.savingsPercent?.toFixed(1) ?? '-'}%` : ''}\n`)
}

if (existing) {
  const token = readFileSync(resolve(arg('token-file', '/home/ponchale/.midopress/admin.token')), 'utf8').trim()
  const admin = arg('admin', 'http://127.0.0.1:9998')
  const get = async path => {
    const response = await fetch(admin + '/api/v1' + path, { headers: { Authorization: 'Bearer ' + token } })
    if (!response.ok) throw new Error('admin endpoint failed: ' + response.status)
    return response.json()
  }
  const about = await get('/about')
  const config = await get('/config')
  report.live = { version: about.version, codecs: about.codecs, profile: config.default_profile, proxy: arg('proxy', 'http://127.0.0.1:9999') }
  const service = { proxyURL: report.live.proxy, stats: () => get('/stats'), caFile: arg('ca', '/home/ponchale/.midopress/ca.crt') }
  for (const site of sites) { if (stopping) break; await runVisit(site, service, config.default_profile, 'live') }
} else {
  for (const [index, site] of sites.entries()) {
    if (stopping) break
    if (!skipDirect && (index % 2 === 0 || directOnly)) await runVisit(site, null, 'direct', 'cold')
    if (!directOnly) {
      for (const profile of index % 2 === 0 ? profiles : [...profiles].reverse()) {
        if (stopping || have(site.id, profile, 'cold') && have(site.id, profile, 'warm')) continue
        const service = await startServer(bin, { profile, args: serverConfig ? ['-config', serverConfig] : [], env: {
          LD_LIBRARY_PATH: nativeLibs, MIDOPRESS_IMAGE_API_ENABLED: 'false',
          MIDOPRESS_WEB_FLOWS: 'false', MIDOPRESS_WEB_DOMAINS: 'true',
        } })
        try {
          await sleep(2000)
          const about = await service.about()
          const effective = (await service.config()).profiles.find(p => p.name === profile)
          if (staticWait !== undefined && effective?.text.static_wait_ms !== Number(staticWait)) throw new Error('static wait override was not applied')
          const extra = { server: { version: about.version, codecs: about.codecs, backend: about.backend,
            profile: effective, staticWaitOverride: staticWait ?? null } }
          await runVisit(site, service, profile, 'cold', extra)
          const settled = await settle(service, { max: 20000 })
          await runVisit(site, service, profile, 'warm', { ...extra, backgroundSettled: settled })
        } finally { await service.stop() }
      }
    }
    if (!skipDirect && index % 2 !== 0 && !directOnly) await runVisit(site, null, 'direct', 'cold')
  }
}
report.finished = new Date().toISOString()
report.stopped = stopping
checkpoint()
