import { spawn } from 'node:child_process'
import { readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

const arg = (name, fallback) => {
  const i = process.argv.indexOf('--' + name)
  return i < 0 ? fallback : process.argv[i + 1]
}
const out = resolve(arg('out', 'artifacts/continuous'))
const admin = arg('admin', 'http://127.0.0.1:9998')
const proxy = arg('proxy', 'http://127.0.0.1:9999')
const tokenFile = arg('token-file', '/home/ponchale/.midopress/admin.token')
const ca = arg('ca', '/home/ponchale/.midopress/ca.crt')
const browser = arg('chromium', '/tmp/midopress-browsers/chromium_headless_shell-1255/chrome-headless-shell-linux64/chrome-headless-shell')
const interval = Math.max(2000, Number(arg('interval-ms', '4000')))
const profiles = ['balanced', 'saver', 'extreme', 'lossless']
const sites = ['wikipedia', 'github', 'amazon', 'alibaba', 'aliexpress', 'youtube', 'google', 'facebook', 'pinterest', 'bbc', 'duckduckgo', 'eltiempo']
const children = new Set()
let stopping = false
let sequence = 0
mkdirSync(out, { recursive: true })
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => {
  stopping = true
  for (const child of children) child.kill('SIGTERM')
})
const log = event => process.stdout.write(JSON.stringify({ time: new Date().toISOString(), ...event }) + '\n')
async function api(path, body) {
  const token = readFileSync(tokenFile, 'utf8').trim()
  const r = await fetch(admin + '/api/v1' + path, { method: body ? 'PUT' : 'GET',
    headers: { Authorization: 'Bearer ' + token, ...(body ? { 'Content-Type': 'application/json' } : {}) },
    body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(5000) })
  if (!r.ok) throw new Error('admin status ' + r.status)
  return r.json()
}
function run(command, args, timeout) {
  return new Promise(resolve => {
    const child = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'] })
    children.add(child)
    let stdout = ''
    const timer = setTimeout(() => child.kill('SIGKILL'), timeout)
    child.stdout.on('data', data => { if (stdout.length < 65536) stdout += data })
    child.stderr.on('data', () => {})
    child.on('error', () => {})
    child.on('close', code => {
      clearTimeout(timer)
      children.delete(child)
      resolve({ code, stdout })
    })
  })
}
async function heartbeat() {
  while (!stopping) {
    const start = Date.now()
    const profile = profiles[sequence++ % profiles.length]
    const r = await run('curl', ['--silent', '--show-error', '--fail', '--max-time', '20', '--max-filesize', '2097152',
      '--noproxy', '', '--proxy', proxy, '--cacert', ca, '--header', 'Accept: image/avif,image/webp,image/jpeg',
      '--header', 'X-MidoPress-Profile: ' + profile, '--output', '/dev/null', '--write-out', '%{json}',
      'https://www.gstatic.com/webp/gallery/1.jpg'], 22000)
    let timing = {}
    try { timing = JSON.parse(r.stdout) } catch {}
    log({ event: 'image', profile, status: timing.http_code, exit: r.code, bytes: timing.size_download,
      ms: Number(timing.time_total) * 1000, tls_verified: timing.ssl_verify_result === 0 })
    try {
      const s = await api('/stats')
      writeFileSync(join(out, 'status.json'), JSON.stringify({ updated: new Date().toISOString(), sequence,
        totals: s.totals, connections: s.connections, cpu: s.cpu, by_profile: s.by_profile }, null, 2) + '\n')
    } catch { log({ event: 'admin_unavailable' }) }
    await sleep(Math.max(100, interval - (Date.now() - start)))
  }
}
async function browsing() {
  let cycle = 0
  while (!stopping) {
    const site = sites[cycle % sites.length]
    const dir = join(out, 'visit-' + String(cycle++).padStart(6, '0'))
    const r = await run(process.execPath, [resolve(import.meta.dirname, 'popular.mjs'), '--existing', '--sites', site,
      '--admin', admin, '--proxy', proxy, '--token-file', tokenFile, '--chromium', browser,
      '--dwell-ms', '2500', '--scrolls', '2', '--out', dir], 65000)
    log({ event: 'navigation', site, exit: r.code })
    const old = readdirSync(out).filter(p => /^visit-\d+$/.test(p)).sort().slice(0, -12)
    for (const p of old) rmSync(join(out, p), { recursive: true, force: true })
    await sleep(60000)
  }
}
async function monitor() {
  while (!stopping) {
    try {
      await api('/settings', { domains: true, flows: true, capture: false })
      const about = await api('/about')
      log({ event: 'service', started: about.started, codecs: about.codecs })
    } catch { log({ event: 'service_unavailable' }) }
    await sleep(30000)
  }
}
log({ event: 'started', interval_ms: interval, proxy, admin })
await Promise.all([heartbeat(), browsing(), monitor()])
