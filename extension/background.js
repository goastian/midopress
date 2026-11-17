if (typeof importScripts === 'function') importScripts('client.js')
const api = globalThis.chrome ?? globalThis.browser
const jobs = []
let running = 0
function schedule(task) {
  return new Promise(resolve => {
    if (jobs.length >= 64) { resolve({ code: 'queue_full' }); return }
    jobs.push({ task, resolve })
    drain()
  })
}
function drain() {
  while (running < 3 && jobs.length) {
    const { task, resolve } = jobs.shift()
    running++
    Promise.resolve().then(task).catch(() => ({ code: 'unavailable' })).then(resolve).finally(() => { running--; drain() })
  }
}
api.runtime.onMessage.addListener((message, sender, reply) => {
  if (sender.id !== api.runtime.id) return false
  if (message?.type === 'optimize' && sender.tab && /^https?:/.test(sender.url ?? '')) {
    schedule(async () => {
      const { config } = await api.storage.local.get('config')
      if (!config) return { code: 'not_configured' }
      return MidoPressClient.optimize(MidoPressClient.settings(config), message.image)
    }).then(reply)
    return true
  }
  return false
})
