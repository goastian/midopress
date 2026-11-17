import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { blockingVulnerabilities } from './audit.mjs'

const policy = { ...JSON.parse(readFileSync(new URL('../audit-policy.json', import.meta.url))), approved: true }
const lock = { packages: { 'node_modules/picomatch': { version: '4.0.7' } } }
const now = new Date('2026-11-17T00:00:00Z')
const report = () => ({
  auditReportVersion: 2,
  vulnerabilities: {
    picomatch: { severity: 'high', fixAvailable: false, via: [{ name: 'picomatch', url: policy.advisory }] },
    vite: { severity: 'high', via: ['picomatch'] },
  },
})

test('only the approved build-only advisory and its dependants are excepted', () => {
  assert.deepEqual(blockingVulnerabilities(report(), policy, lock, now), [])
  for (const fix of [
    { name: 'vue-router', version: '4.6.5', isSemVerMajor: true },
    { name: 'vite', version: '6.2.7', isSemVerMajor: true },
  ]) {
    const removal = report()
    removal.vulnerabilities.picomatch.fixAvailable = fix
    assert.deepEqual(blockingVulnerabilities(removal, policy, lock, now), [])
  }
  const changed = report()
  changed.vulnerabilities.picomatch.via.push({ name: 'picomatch', url: 'https://github.com/advisories/another' })
  assert.deepEqual(blockingVulnerabilities(changed, policy, lock, now), ['picomatch', 'vite'])
})

test('approval, expiry, version changes and available fixes remain blocking', () => {
  for (const [p, l, date, data] of [
    [{ ...policy, approved: false }, lock, now, report()],
    [policy, lock, new Date(policy.expires), report()],
    [policy, { packages: { 'node_modules/picomatch': { version: '4.0.8' } } }, now, report()],
    [policy, lock, now, { ...report(), vulnerabilities: { picomatch: { ...report().vulnerabilities.picomatch, fixAvailable: true } } }],
    [policy, lock, now, { ...report(), vulnerabilities: { picomatch: { ...report().vulnerabilities.picomatch, fixAvailable: { name: 'picomatch', version: '4.0.8', isSemVerMajor: false } } } }],
  ]) {
    assert.ok(blockingVulnerabilities(data, p, l, date).includes('picomatch'))
  }
})

test('unrelated advisories, cyclic metadata and audit errors fail closed', () => {
  const changed = report()
  changed.vulnerabilities.other = { severity: 'critical', via: [{ name: 'other', url: policy.advisory }] }
  changed.vulnerabilities.cycle = { severity: 'high', via: ['cycle'] }
  assert.deepEqual(blockingVulnerabilities(changed, policy, lock, now), ['other', 'cycle'])
  assert.throws(() => blockingVulnerabilities({ error: { code: 'EAUDIT' } }, policy, lock, now))
})
