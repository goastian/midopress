// Helpers to run a throw-away MidoPress and read what it did.
import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'

export const TOKEN = 'scenario-token-0123456789abcdef0123456789abcdef0123456789abcdef'

function freePort() {
  return new Promise((resolve, reject) => {
    const s = net.createServer()
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address()
      s.close(() => resolve(port))
    })
    s.on('error', reject)
  })
}

async function until(fn, ms, what) {
  const end = Date.now() + ms
  for (;;) {
    try {
      if (await fn()) return
    } catch {}
    if (Date.now() > end) throw new Error('timed out waiting for ' + what)
    await new Promise((r) => setTimeout(r, 100))
  }
}

/**
 * Starts the binary with its own state directory, a CA and ports nobody else
 * uses, with profile as the default one, and tracking of hosts on.
 */
export async function startServer(bin, { profile = 'balanced', env = {}, args = [] } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'midopress-scn-'))
  writeFileSync(join(dir, 'admin.token'), TOKEN + '\n', { mode: 0o600 })
  const ca = spawnSync(bin, ['ca', 'init', '-dir', dir], { stdio: 'ignore' })
  if (ca.status !== 0) throw new Error('ca init failed')
  const proxy = await freePort()
  const admin = await freePort()
  const child = spawn(bin, ['-host', `127.0.0.1:${proxy}`, '-ca', join(dir, 'ca.crt'), '-cakey', join(dir, 'ca.key'), '-profile', profile, ...args], {
    stdio: ['ignore', 'ignore', 'pipe'],
    env: { ...process.env, MIDOPRESS_WEB_ADDR: `127.0.0.1:${admin}`, MIDOPRESS_WEB_STATE_DIR: dir, ...env },
  })
  let stderr = ''
  child.stderr.on('data', (d) => (stderr += d))
  const base = `http://127.0.0.1:${admin}/api/v1`
  const headers = { Authorization: 'Bearer ' + TOKEN }
  await until(async () => (await fetch(base + '/health')).ok, 20000, 'the administration server')
  const get = async (path) => {
    const r = await fetch(base + path, { headers })
    if (!r.ok) throw new Error(`${path}: ${r.status}`)
    return r.json()
  }
  await fetch(base + '/settings', { method: 'PUT', headers: { ...headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ domains: true }) })
  return {
    dir, proxyPort: proxy, adminPort: admin,
    proxyURL: `http://127.0.0.1:${proxy}`,
    stats: () => get('/stats'),
    about: () => get('/about'),
    missed: (reason, limit = 50) => get(`/stats/missed?reason=${encodeURIComponent(reason)}&limit=${limit}`),
    metrics: async () => (await fetch(`http://127.0.0.1:${proxy}/metrics`)).text(),
    stderr: () => stderr,
    async stop() {
      child.kill('SIGTERM')
      await new Promise((r) => {
        child.on('exit', r)
        setTimeout(() => (child.kill('SIGKILL'), r()), 5000)
      })
      rmSync(dir, { recursive: true, force: true })
    },
  }
}

/** Reads one number out of the Prometheus text. */
export function metric(text, name, labels = {}) {
  const want = Object.entries(labels).map(([k, v]) => `${k}="${v}"`)
  for (const line of text.split('\n')) {
    if (line.startsWith('#') || !line.startsWith(name)) continue
    const rest = line.slice(name.length)
    if (rest[0] !== ' ' && rest[0] !== '{') continue
    if (want.every((w) => line.includes(w))) return Number(line.slice(line.lastIndexOf(' ') + 1))
  }
  return 0
}

/** Waits until the work MidoPress does after a response went out has finished. */
export async function settle(srv, { max = 90000 } = {}) {
  const end = Date.now() + max
  let quiet = 0
  while (Date.now() < end) {
    const m = await srv.metrics()
    const busy = metric(m, 'midopress_background_queue') + metric(m, 'midopress_cpu_slots_busy')
    quiet = busy === 0 ? quiet + 1 : 0
    if (quiet >= 4) return true
    await new Promise((r) => setTimeout(r, 500))
  }
  return false
}

export const sleep = (ms) => new Promise((r) => setTimeout(r, ms))
