import { readFileSync, writeFileSync, appendFileSync, statSync, renameSync, mkdirSync } from 'node:fs'
import { resolve, join } from 'node:path'
import { setTimeout as sleep } from 'node:timers/promises'

const output = resolve('artifacts/continuous')
mkdirSync(output, { recursive: true })
const file = join(output, 'observations.jsonl')
let previous
let started
let stop = false
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => { stop = true })
async function get(path) {
  const token = readFileSync('/home/ponchale/.midopress/admin.token', 'utf8').trim()
  const response = await fetch('http://127.0.0.1:9998/api/v1' + path, {
    headers: { Authorization: 'Bearer ' + token }, signal: AbortSignal.timeout(4000) })
  if (!response.ok) throw new Error('HTTP ' + response.status)
  return response.json()
}
function record(value) {
  try { if (statSync(file).size > 4 * 1024 * 1024) renameSync(file, file + '.previous') } catch {}
  appendFileSync(file, JSON.stringify({ time: new Date().toISOString(), ...value }) + '\n')
}
let iteration = 0
while (!stop) {
  try {
    const s = await get('/stats')
    const boot = Math.round((Date.parse(s.time) - s.uptime_seconds * 1000) / 1000)
    const reset = started !== undefined && started !== boot
    const delta = previous && !reset ? {
      requests: s.totals.requests - previous.totals.requests,
      saved: s.totals.saved - previous.totals.saved,
      errors: s.connections.upstream_errors - previous.connections.upstream_errors,
    } : null
    record({ event: reset ? 'service_restarted' : 'snapshot', totals: s.totals, delta, cpu: s.cpu,
      connections: s.connections, by_kind: s.by_kind, by_profile: s.by_profile })
    previous = s
    started = boot
    if (iteration++ % 5 === 0) {
      const [about, logs] = await Promise.all([get('/about'), get('/log')])
      record({ event: 'runtime', started: about.started, codecs: about.codecs, sandbox: about.sandbox,
        warnings: (logs.lines ?? []).filter(l => ['warn', 'error'].includes(String(l.level).toLowerCase())).slice(-10).map(l => ({ level: l.level, message: l.message })) })
    }
    writeFileSync(join(output, 'observer.json'), JSON.stringify({ updated: new Date().toISOString(), running: true,
      requests: s.totals.requests, saved: s.totals.saved, boot, reset, delta }, null, 2) + '\n')
  } catch (error) { record({ event: 'unavailable', message: String(error.message).slice(0, 100) }) }
  await sleep(2000)
}
