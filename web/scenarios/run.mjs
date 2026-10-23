// Runs the scenarios against a MidoPress binary, once per profile, and writes
// what each one cost in bytes, by kind of content, to artifacts/scenarios/.
//
//   node scenarios/run.mjs --bin ../bin/midopress --label after \
//        --profiles balanced,saver,extreme --only news,github [--append]
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { chromium } from '@playwright/test'
import { scenarios } from './scenarios.mjs'
import { settle, sleep, startServer } from './lib.mjs'

const arg = (name, def) => {
  const i = process.argv.indexOf('--' + name)
  return i > 0 ? process.argv[i + 1] : def
}
const bin = resolve(arg('bin', '../bin/midopress'))
const label = arg('label', 'run')
const profiles = arg('profiles', 'balanced,saver,extreme').split(',')
const only = arg('only', Object.keys(scenarios).join(',')).split(',')
const out = resolve(arg('out', '../artifacts/scenarios'))
mkdirSync(out, { recursive: true })

const kinds = ['image', 'text', 'font', 'media', 'other']

function digest(s) {
  const k = s.by_kind
  const row = (n) => ({ requests: k[n]?.requests ?? 0, bytes_in: k[n]?.bytes_in ?? 0, bytes_out: k[n]?.bytes_out ?? 0 })
  const comp = ['image', 'text', 'font'].reduce(
    (a, n) => ({ bytes_in: a.bytes_in + row(n).bytes_in, bytes_out: a.bytes_out + row(n).bytes_out }),
    { bytes_in: 0, bytes_out: 0 },
  )
  return {
    kinds: Object.fromEntries(kinds.map((n) => [n, row(n)])),
    compressible: { ...comp, saved_percent: comp.bytes_in ? ((comp.bytes_in - comp.bytes_out) / comp.bytes_in) * 100 : 0 },
    totals: s.totals,
    tunnel: { up: s.connections.tunnel_bytes_up, down: s.connections.tunnel_bytes_down },
    by_reason: s.by_reason,
    by_cause: s.by_cause,
    top_missed: s.top_missed,
    cache: s.cache,
  }
}

async function browse(srv, scenario, note) {
  const browser = await chromium.launch({
    proxy: { server: srv.proxyURL },
    // Chromium does not have the CA of the throw-away server in its store. This
    // flag makes it behave as a browser that trusts it, without touching the
    // certificate store of the machine. Without it every host is rejected once
    // and then passed through untouched, which measures nothing.
    args: ['--ignore-certificate-errors', ...(scenario.launch?.args ?? [])],
  })
  try {
    const ctx = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1280, height: 800 } })
    const page = await ctx.newPage()
    await scenario.run(page, note)
    await sleep(1500)
  } finally {
    await browser.close()
  }
}

const results = existsSync(`${out}/${label}.json`) && process.argv.includes('--append') ? JSON.parse(readFileSync(`${out}/${label}.json`, 'utf8')) : []
const attempts = Number(arg('attempts', '3'))

// A run counts when the site gave what the scenario is about: a page that came
// back as a block page or an empty shell measures nothing.
function enough(scenario, d) {
  const m = scenario.min ?? {}
  const video = d.kinds.media.bytes_in + d.kinds.other.bytes_in
  return (
    d.compressible.bytes_in >= (m.compressible ?? 0) &&
    d.kinds.image.bytes_in >= (m.image ?? 0) &&
    d.kinds.media.bytes_in >= (m.media ?? 0) &&
    video >= (m.video ?? 0)
  )
}

for (const name of only) {
  const scenario = scenarios[name]
  if (!scenario) throw new Error('no scenario ' + name)
  for (const profile of profiles) {
    let run
    for (let attempt = 1; attempt <= attempts; attempt++) {
      const srv = await startServer(bin, { profile })
      const about = await srv.about()
      run = { scenario: name, title: scenario.title, profile, label, bin, version: about.version, backend: about.backend, native: about.native, started: new Date().toISOString(), attempt }
      process.stderr.write(`${label} ${name} ${profile} #${attempt} ... `)
      try {
        const note = {}
        await browse(srv, scenario, note)
        run.note = note
        run.settled = await settle(srv)
        run.cold = digest(await srv.stats())
        run.missedBusy = (await srv.missed('busy')).hosts
        run.missedNoTransform = (await srv.missed('no-transform')).hosts
        if (scenario.warm && enough(scenario, run.cold)) {
          const before = run.cold
          await browse(srv, scenario, {})
          await settle(srv)
          const s = digest(await srv.stats())
          run.warm = s
          run.warmDelta = {
            compressible_in: s.compressible.bytes_in - before.compressible.bytes_in,
            compressible_out: s.compressible.bytes_out - before.compressible.bytes_out,
          }
        }
        run.complete = enough(scenario, run.cold)
        process.stderr.write(`compressible ${run.cold.compressible.bytes_in} -> ${run.cold.compressible.bytes_out} (${run.cold.compressible.saved_percent.toFixed(1)}%)${run.complete ? '' : ' too little, the site did not give the page'}\n`)
      } catch (e) {
        run.error = String(e)
        run.complete = false
        process.stderr.write('failed: ' + e + '\n')
      } finally {
        run.log = srv.stderr().split('\n').slice(-5).join('\n')
        await srv.stop()
      }
      if (run.complete) break
    }
    const at = results.findIndex((x) => x.scenario === name && x.profile === profile)
    if (at >= 0) results.splice(at, 1) // a run of the same scenario and profile replaces the old one
    results.push(run)
    writeFileSync(`${out}/${label}.json`, JSON.stringify(results, null, 2))
  }
}
