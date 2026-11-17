import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { spawnSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'

export function blockingVulnerabilities(report, policy, lock, now = new Date()) {
  if (report.error || report.auditReportVersion !== 2 || !report.vulnerabilities) {
    throw new Error('npm did not return a valid audit report')
  }
  const installed = Object.entries(lock.packages).filter(([path]) => path.endsWith('/picomatch'))
  const allowed = policy.approved === true && now < new Date(policy.expires) &&
    installed.length > 0 && installed.every(([, pkg]) => pkg.version === policy.version)
  const excepted = (name, seen = new Set()) => {
    const vulnerability = report.vulnerabilities[name]
    if (!allowed || !vulnerability?.via?.length || seen.has(name)) return false
    const next = new Set([...seen, name])
    const fix = vulnerability.fixAvailable
    const removalOnly = fix && Object.entries(policy.removalSuggestion).every(([key, value]) => fix[key] === value)
    return vulnerability.via.every(via => typeof via === 'string'
      ? excepted(via, next)
      : name === policy.package && via.name === policy.package && via.url === policy.advisory &&
        (fix === false || removalOnly))
  }
  return Object.entries(report.vulnerabilities)
    .filter(([, vulnerability]) => !['info', 'low', 'moderate'].includes(vulnerability.severity))
    .filter(([name]) => !excepted(name))
    .map(([name]) => name)
}

function main() {
  const policy = JSON.parse(readFileSync(new URL('../audit-policy.json', import.meta.url), 'utf8'))
  const lock = JSON.parse(readFileSync(new URL('../package-lock.json', import.meta.url), 'utf8'))
  const result = spawnSync('npm', ['audit', '--audit-level=high', '--json'], {
    cwd: new URL('..', import.meta.url), encoding: 'utf8', timeout: 120_000, maxBuffer: 8 << 20,
  })
  if (result.error || ![0, 1].includes(result.status)) {
    throw result.error ?? new Error(result.stderr || `npm audit exited with ${result.status}`)
  }
  console.log(result.stdout)
  const blocked = blockingVulnerabilities(JSON.parse(result.stdout), policy, lock)
  if (blocked.length) {
    console.error(`Blocking vulnerabilities: ${blocked.join(', ')}`)
    process.exitCode = 1
  } else if (policy.approved) {
    console.log(`Build-only exception: ${policy.advisory}; expires ${policy.expires}. ${policy.reason}`)
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main()
}
