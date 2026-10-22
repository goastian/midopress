// Starts a throw-away MidoPress for the end to end tests: its own state
// directory, a known admin token, a CA, and ports that nobody else uses.
import { spawn, spawnSync } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { TOKEN, PROXY } from './env.mjs'

const bin = resolve(import.meta.dirname, '../../midopress')
const dir = mkdtempSync(join(tmpdir(), 'midopress-e2e-'))
writeFileSync(join(dir, 'admin.token'), TOKEN + '\n', { mode: 0o600 })
const ca = spawnSync(bin, ['ca', 'init', '-dir', dir], { stdio: 'inherit' })
if (ca.status !== 0) process.exit(1)

const child = spawn(
  bin,
  ['-host', PROXY, '-ca', join(dir, 'ca.crt'), '-cakey', join(dir, 'ca.key')],
  {
    stdio: 'inherit',
    env: { ...process.env, MIDOPRESS_WEB_ADDR: '127.0.0.1:29998', MIDOPRESS_WEB_STATE_DIR: dir },
  },
)
for (const sig of ['SIGINT', 'SIGTERM']) process.on(sig, () => child.kill('SIGTERM'))
child.on('exit', (code) => process.exit(code ?? 0))
