// Prints the results of run.mjs as Markdown tables: what each scenario cost, in
// bytes, by kind of content, and what was saved of the part that can be shrunk.
//
//   node scenarios/table.mjs ../artifacts/scenarios/baseline.json [after.json ...]
import { readFileSync } from 'node:fs'
import { basename } from 'node:path'

const mb = (n) => (n / 1e6).toFixed(2)
const pct = (a, b) => (a > 0 ? (((a - b) / a) * 100).toFixed(1) + '%' : '-')

for (const file of process.argv.slice(2)) {
  const runs = JSON.parse(readFileSync(file, 'utf8'))
  console.log(`\n### ${basename(file, '.json')}\n`)
  console.log('| scenario | profile | image in > out (MB) | text in > out | font | video and audio | other | tunnels | saved of images, text, fonts |')
  console.log('|---|---|---:|---:|---:|---:|---:|---:|---:|')
  for (const r of runs) {
    if (!r.cold) {
      console.log(`| ${r.scenario} | ${r.profile} | failed: ${r.error ?? ''} | | | | | | |`)
      continue
    }
    const k = r.cold.kinds
    const pair = (n) => `${mb(k[n].bytes_in)} > ${mb(k[n].bytes_out)}`
    const note = r.complete === false ? ' (the site gave too little)' : ''
    console.log(
      `| ${r.scenario}${note} | ${r.profile} | ${pair('image')} | ${pair('text')} | ${mb(k.font.bytes_in)} | ${mb(k.media.bytes_in)} | ${mb(k.other.bytes_in)} | ${mb(r.cold.tunnel.down)} | ` +
        `${pct(r.cold.compressible.bytes_in, r.cold.compressible.bytes_out)} of ${mb(r.cold.compressible.bytes_in)} |`,
    )
  }
}
