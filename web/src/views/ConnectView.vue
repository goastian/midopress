<script setup lang="ts">
import { computed } from 'vue'
import { api } from '../api/client'
import { usePoll } from '../composables/poll'
import { toastError } from '../composables/toast'
import { dateTime } from '../composables/format'
import PagesCopyButton from '../components/PagesCopyButton.vue'
import UiIcon from '../components/UiIcon.vue'

const { data: about, error: aboutError } = usePoll(() => api.about())
const { data: ca, error, loading, reload } = usePoll(() => api.ca())

const host = window.location.hostname
const port = computed(() => /:(\d+)\]?$/.exec(about.value?.proxy_addr ?? '')?.[1] ?? '9999')
const pacUrl = computed(() => `http://${host}:${port.value}/proxy.pac`)
const fullPacUrl = computed(() => `http://${host}:${port.value}/proxy-full.pac`)
const checkUrl = computed(() => `http://${ca.value?.magic_host || 'midopress.it'}`)

const fingerprint = computed(() => {
  const hex = (ca.value?.sha256 ?? '').replace(/[^0-9a-fA-F]/g, '').toUpperCase()
  return hex.match(/.{1,2}/g)?.join(' ') ?? ''
})
const fingerprintPlain = computed(() => fingerprint.value.replaceAll(' ', ':'))

const daysLeft = computed(() => {
  if (!ca.value?.not_after) return null
  return Math.floor((new Date(ca.value.not_after).getTime() - Date.now()) / 86400000)
})

async function download(kind: 'pem' | 'der' | 'p12') {
  try {
    await api.downloadCA(kind)
  } catch (e) {
    toastError(e)
  }
}

function selectAll(e: Event) {
  ;(e.target as HTMLInputElement).select()
}

const pacPlatforms: { name: string; steps: string[] }[] = [
  {
    name: 'Windows 11',
    steps: ['Open Settings, Network & internet, Proxy.', 'Under Automatic proxy setup, switch on Use setup script and choose Set up (or Edit).', 'Paste the address into Script address and Save.'],
  },
  {
    name: 'macOS',
    steps: ['Open System Settings, Network, and choose your connection (Wi-Fi or Ethernet), then Details.', 'Open Proxies and switch on Automatic proxy configuration.', 'Paste the address into the URL field and press OK, then Apply.'],
  },
  {
    name: 'iPhone and iPad (iOS, iPadOS)',
    steps: ['Open Settings, Wi-Fi, and tap the (i) next to your network.', 'Scroll to Configure Proxy and choose Automatic.', 'Paste the address into URL and Save.'],
  },
  {
    name: 'Android',
    steps: ['Open Settings, Network & internet, Wi-Fi, and open your network (the gear, or press and hold, then Modify network).', 'Open Advanced options, then Proxy, and choose Proxy auto-config.', 'Paste the address into PAC URL and Save. The labels differ a little between makers.'],
  },
  {
    name: 'Firefox and Midori',
    steps: ['Open Settings and find Network Settings (in General on desktop).', 'Choose Automatic proxy configuration URL.', 'Paste the address and press OK. Firefox then follows the address on its own, whatever the system says.'],
  },
  {
    name: 'Linux (GNOME)',
    steps: ['Open Settings, Network, and then Network Proxy.', 'Choose Automatic.', 'Paste the address into Configuration URL.'],
  },
]

const setupYaml = `mitm:
  ca_cert: /home/you/.midopress/ca.crt
  ca_key: /home/you/.midopress/ca.key`

const certPlatforms: { name: string; steps: string[]; command?: string }[] = [
  {
    name: 'Android',
    steps: [
      'Open the install page (address above) in the browser and download ca.crt, or copy the PEM file to the phone.',
      'Open Settings, then Security (Security and privacy, More security settings), Encryption and credentials, Install a certificate, CA certificate. Confirm the warning and pick the file.',
      'Chrome and most browsers trust it from then on. Apps that target Android 7 or later ignore user certificates unless they opt in, so those apps keep their own trust and stay unfiltered.',
    ],
  },
  {
    name: 'iOS and iPadOS',
    steps: [
      'Open the install page (address above) in Safari and allow the download of the configuration profile.',
      'Settings, General, VPN and Device Management: open the downloaded profile and install it.',
      'Settings, General, About, Certificate Trust Settings: switch on full trust for MidoPress CA. Without this step the certificate is installed but not trusted.',
    ],
  },
  {
    name: 'Windows',
    steps: [
      'Download ca.crt (PEM) and double-click it, then Install Certificate.',
      'Choose Local Machine (needs administrator) or Current User, then Place all certificates in the following store, Trusted Root Certification Authorities.',
      'Edge and Chrome use the Windows store. Firefox does not, see the Firefox section.',
    ],
    command: 'certutil -addstore -f Root midopress-ca.crt',
  },
  {
    name: 'macOS',
    steps: [
      'Download ca.crt and open it; Keychain Access adds it to the login or System keychain.',
      'Double-click the certificate, expand Trust and set When using this certificate to Always Trust.',
      'Safari and Chrome use the keychain. Firefox does not, see the Firefox section.',
    ],
    command: 'sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain midopress-ca.crt',
  },
  {
    name: 'Linux',
    steps: [
      'System store on Debian and Ubuntu: copy the PEM file to /usr/local/share/ca-certificates/midopress-ca.crt and run update-ca-certificates.',
      'On Fedora and Arch use /etc/pki/ca-trust/source/anchors/ (or /etc/ca-certificates/trust-source/anchors/) with update-ca-trust.',
      'Chrome and Chromium read the NSS database of the user instead; add it there with certutil (package libnss3-tools). Firefox has its own store.',
    ],
    command: 'sudo cp midopress-ca.crt /usr/local/share/ca-certificates/ && sudo update-ca-certificates',
  },
  {
    name: 'Firefox and Midori',
    steps: [
      'Desktop: Settings, Privacy and Security, Certificates, View Certificates, Authorities, Import. Pick the PEM file and tick Trust this CA to identify websites.',
      'Alternatively set security.enterprise_roots.enabled to true in about:config so that it follows the system store.',
      'Android: the browser only reads user certificates when Use third party CA certificates is on, in Secret Settings (tap the version number in About five times).',
    ],
  },
]
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>Connect</h1>
        <p>Point a device at MidoPress. One address, nothing to install for plain HTTP.</p>
      </div>
      <button class="btn small" :disabled="loading" @click="reload">Refresh</button>
    </div>

    <p v-if="error" class="notice err">{{ error }}</p>
    <p v-if="aboutError" class="notice err">{{ aboutError }}</p>

    <section class="sec">
      <div class="sec-head"><h2>1. Paste this address</h2></div>
      <p class="lede">
        Put it in the <strong>Automatic proxy configuration</strong> field of the device (some systems call it Proxy auto-config URL or Use setup script).
        The device then sends its web traffic through MidoPress by itself. It works without any certificate.
      </p>
      <div class="address">
        <label class="sr-only" for="pac-url">Proxy auto-configuration address</label>
        <input id="pac-url" class="input mono big" readonly :value="pacUrl" @focus="selectAll" />
        <PagesCopyButton :value="pacUrl" />
      </div>
      <p class="hint host-hint">
        Built from <span class="mono">{{ host }}</span>, the name this page was opened with. A device on another network may need the proxy host's own address instead.
      </p>

      <h3 class="sub-h">Where it goes on each system</h3>
      <div class="plat">
        <details v-for="p in pacPlatforms" :key="p.name">
          <summary><UiIcon name="chevron" :size="12" />{{ p.name }}</summary>
          <ol>
            <li v-for="s in p.steps" :key="s">{{ s }}</li>
          </ol>
        </details>
      </div>
      <p class="hint">These steps are written from each system's documentation and have not been tested on devices.</p>
    </section>

    <section class="sec">
      <div class="sec-head"><h2>2. What this does and does not do</h2></div>
      <div class="cols honest">
        <div>
          <h3>With the address alone</h3>
          <p>MidoPress shrinks plain HTTP: text, images and fonts from sites that still use <span class="mono">http://</span>. Everything else is left exactly as it is, and nothing breaks.</p>
        </div>
        <div>
          <h3>What it cannot do</h3>
          <p>
            HTTPS is encrypted from the site to the device, so a proxy only sees that a connection was made, not what is inside. Shrinking it
            needs the device to trust the MidoPress certificate; then use the second address in step 3. Unlike a DNS service, a proxy cannot see inside HTTPS without that certificate.
          </p>
        </div>
      </div>
    </section>

    <section class="sec">
      <div class="sec-head">
        <h2>3. Also shrink HTTPS</h2>
        <span v-if="ca" class="sub">{{ ca.available ? 'Needs the certificate on each device' : 'Off: no certificate authority' }}</span>
      </div>

      <div v-if="!ca" class="skeleton" role="status" aria-label="Loading"><i /><i /><i /></div>

      <template v-else>
        <div class="address" :class="{ off: !ca.available }">
          <label class="sr-only" for="full-url">Second address, for HTTP and HTTPS</label>
          <input id="full-url" class="input mono big" readonly :disabled="!ca.available" :value="fullPacUrl" @focus="selectAll" />
          <PagesCopyButton v-if="ca.available" :value="fullPacUrl" />
          <button v-else class="btn" disabled>Copy</button>
        </div>

        <template v-if="!ca.available">
          <div class="stack-sm off-note">
            <p>
              This address does not exist yet because HTTPS interception is off: no certificate authority is configured, so HTTPS connections are tunnelled untouched.
              To turn it on, create a certificate authority, point two configuration keys at it and restart MidoPress.
            </p>
            <ol class="steps">
              <li>
                Create the authority (valid 10 years by default; <code>-days</code>, <code>-name</code> and <code>-dir</code> change that):
                <pre class="pre">midopress ca init</pre>
              </li>
              <li>
                Set <code>mitm.ca_cert</code> and <code>mitm.ca_key</code> in the configuration file:
                <pre class="pre">{{ setupYaml }}</pre>
                <PagesCopyButton :value="setupYaml" label="Copy snippet" />
              </li>
              <li>Restart MidoPress and reload this page.</li>
            </ol>
            <p class="hint">The same can be set with the flags <code>-ca</code> and <code>-cakey</code> or the variables <code>MIDOPRESS_MITM_CA_CERT</code> and <code>MIDOPRESS_MITM_CA_KEY</code>.</p>
          </div>
        </template>

        <template v-else>
          <p class="hint host-hint">Use this address instead of the first one, on devices that trust the certificate below. It sends HTTP and HTTPS through MidoPress.</p>

          <h3 class="sub-h">Certificate</h3>
          <dl class="dl">
            <dt>Subject</dt>
            <dd class="mono">{{ ca.subject }}</dd>
            <dt>Valid from</dt>
            <dd class="mono">{{ dateTime(ca.not_before) }}</dd>
            <dt>Valid until</dt>
            <dd class="mono">
              {{ dateTime(ca.not_after) }}
              <span v-if="daysLeft !== null" :class="daysLeft < 30 ? 'bad' : 'muted'">
                ({{ daysLeft < 0 ? `expired ${-daysLeft} days ago` : `${daysLeft} days left` }})
              </span>
            </dd>
            <dt>SHA-256</dt>
            <dd>
              <div class="row tight fp">
                <code class="grow mono">{{ fingerprint }}</code>
                <PagesCopyButton :value="fingerprintPlain" />
              </div>
              <span class="hint">Compare it with the one shown on the device before trusting the certificate.</span>
            </dd>
            <dt>Install page</dt>
            <dd>
              <div class="row tight">
                <a class="mono grow" :href="ca.page_url" target="_blank" rel="noopener">{{ ca.page_url }}</a>
                <PagesCopyButton :value="ca.page_url" />
              </div>
              <span class="hint">
                Open it from a device that uses the proxy. <span class="mono">{{ ca.magic_host }}</span> is answered by MidoPress and never forwarded, so it does not open from a browser that is not using the proxy.
              </span>
            </dd>
          </dl>

          <h3 class="sub-h">Download</h3>
          <div class="row tight">
            <button class="btn" @click="download('pem')">PEM (.crt)</button>
            <button class="btn" @click="download('der')">DER (.der)</button>
            <button class="btn" @click="download('p12')">PKCS#12 (.p12)</button>
          </div>
          <p class="hint formats">
            PEM is what most systems and Firefox want, DER is the binary form (Windows, Java). The PKCS#12 file is a trust store holding only the certificate, protected with the password <code>midopress</code>. The private key is never downloadable.
          </p>

          <h3 class="sub-h">Trust it on the device</h3>
          <div class="plat">
            <details v-for="p in certPlatforms" :key="p.name">
              <summary><UiIcon name="chevron" :size="12" />{{ p.name }}</summary>
              <ol>
                <li v-for="s in p.steps" :key="s">{{ s }}</li>
              </ol>
              <div v-if="p.command" class="cmd row tight">
                <code class="grow mono">{{ p.command }}</code>
                <PagesCopyButton :value="p.command" />
              </div>
            </details>
          </div>

          <h3 class="sub-h">What trusting it allows</h3>
          <div class="stack-sm safety">
            <p>A device that trusts this authority accepts any HTTPS certificate MidoPress signs with it. That is what lets the proxy see and recompress encrypted pages, and it means whoever holds the private key can impersonate any website to those devices.</p>
            <ul class="plain">
              <li>Install it only on devices you own or administer, and only on those that use this proxy.</li>
              <li>Never share <code>ca.key</code>, and keep it readable by the MidoPress user only. Anyone who gets it can intercept the traffic of every device that trusts the CA.</li>
              <li>If the key may have leaked, remove the certificate from the devices and create a new authority.</li>
              <li>Banking and other sensitive hosts can be excluded from decryption with <code>mitm.passthrough</code> in the configuration, or a rule with action <code>passthrough_tls</code>.</li>
            </ul>
          </div>
        </template>
      </template>
    </section>

    <section class="sec">
      <div class="sec-head"><h2>4. Check this device</h2></div>
      <p>
        Open <a class="mono" :href="checkUrl" target="_blank" rel="noopener">{{ checkUrl }}</a> in a browser on the device. If MidoPress answers with its own page, the device is using the proxy.
        If the browser cannot find the site, it is not.
      </p>
    </section>
  </div>
</template>

<style scoped>
.address {
  display: flex;
  gap: 0.5rem;
  align-items: stretch;
  max-width: 52rem;
}
.address .input {
  flex: 1 1 auto;
  min-width: 0;
}
.address .btn {
  min-height: auto;
}
.input.big {
  font-size: 18px;
  min-height: 52px;
  padding-inline: 0.8rem;
  border-color: var(--ink);
  background: var(--surface);
}
.input.big:disabled {
  border-color: var(--rule-strong);
  color: var(--ink-3);
}
.address :deep(.btn) {
  min-width: 5rem;
  font-size: var(--fs-sm);
}
.address :deep(.btn:not([disabled])) {
  background: var(--ink);
  border-color: var(--ink);
  color: var(--inverse);
}
.host-hint {
  margin-top: 0.5rem;
  max-width: 62ch;
}
.sub-h {
  margin: 1.75rem 0 0.5rem;
}
.plat {
  border-top: 1px solid var(--rule);
}
.plat details {
  border-bottom: 1px solid var(--rule);
}
.plat summary {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  padding: 0.55rem 0;
  cursor: pointer;
  list-style: none;
  font-size: var(--fs-sm);
}
.plat summary::-webkit-details-marker {
  display: none;
}
.plat summary svg {
  color: var(--ink-2);
  transition: transform 0.12s;
}
.plat details[open] summary svg {
  transform: rotate(90deg);
}
.plat summary:focus-visible {
  outline-offset: -2px;
}
.plat ol {
  margin: 0;
  padding: 0 0 0.7rem 2.1rem;
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  font-size: var(--fs-sm);
  max-width: 72ch;
}
.cmd {
  padding: 0 0 0.8rem 1.1rem;
}
.cmd code {
  overflow-wrap: anywhere;
  padding: 0.3rem 0.5rem;
}
.honest h3 {
  margin-bottom: 0.3rem;
}
.honest p {
  max-width: 56ch;
  color: var(--ink-2);
}
.off-note {
  margin-top: 1rem;
}
.steps {
  margin: 0;
  padding-left: 1.2rem;
  display: flex;
  flex-direction: column;
  gap: 0.8rem;
  font-size: var(--fs-sm);
}
.steps .pre {
  margin: 0.3rem 0;
}
.fp code,
.dl a {
  overflow-wrap: anywhere;
}
.formats {
  margin-top: 0.6rem;
  max-width: 70ch;
}
.plain {
  margin: 0;
  padding-left: 1.2rem;
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
}
.safety {
  max-width: 76ch;
  font-size: var(--fs-sm);
}
@media (max-width: 720px) {
  .address {
    flex-direction: column;
  }
  .input.big {
    font-size: 14px;
  }
}
</style>
