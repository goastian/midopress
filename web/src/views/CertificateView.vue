<script setup lang="ts">
import { computed } from 'vue'
import { api } from '../api/client'
import { usePoll } from '../composables/poll'
import { toastError } from '../composables/toast'
import { dateTime } from '../composables/format'
import PagesCopyButton from '../components/PagesCopyButton.vue'

const { data: ca, error, loading, reload } = usePoll(() => api.ca())

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

const setupYaml = `mitm:
  ca_cert: /home/you/.midopress/ca.crt
  ca_key: /home/you/.midopress/ca.key`

const platforms: { name: string; steps: string[]; command?: string }[] = [
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
        <h1>Certificate</h1>
        <p>The authority that lets MidoPress read and shrink HTTPS traffic of devices that trust it.</p>
      </div>
      <button class="btn" :disabled="loading" @click="reload">Refresh</button>
    </div>

    <p v-if="error" class="notice err">{{ error }}</p>

    <section v-if="ca && !ca.available" class="sec">
      <div class="sec-head"><h2>HTTPS interception is off</h2></div>
      <div class="stack-sm">
        <p>No certificate authority is configured, so HTTPS connections are tunnelled untouched: only plain HTTP is optimised. To decrypt and optimise HTTPS, create a CA and point the configuration at it, then restart MidoPress.</p>
        <ol class="steps">
          <li>
            Create the authority (valid 10 years by default; <code>-days</code>, <code>-name</code> and <code>-dir</code> change that):
            <pre class="pre">midopress ca init</pre>
          </li>
          <li>
            Reference it in the configuration file:
            <pre class="pre">{{ setupYaml }}</pre>
            <PagesCopyButton :value="setupYaml" label="Copy snippet" />
          </li>
          <li>Restart MidoPress, then come back here to download the certificate and install it on each device.</li>
        </ol>
        <p class="muted small">The same can be set with the flags <code>-ca</code> and <code>-cakey</code> or the variables <code>MIDOPRESS_MITM_CA_CERT</code> and <code>MIDOPRESS_MITM_CA_KEY</code>.</p>
      </div>
    </section>

    <template v-else-if="ca">
      <section class="sec">
        <div class="sec-head"><h2>Authority</h2></div>
        <dl class="dl">
          <dt>Subject</dt>
          <dd class="mono">{{ ca.subject }}</dd>
          <dt>Valid from</dt>
          <dd class="num">{{ dateTime(ca.not_before) }}</dd>
          <dt>Valid until</dt>
          <dd class="num">
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
        </dl>
      </section>

      <section class="sec">
        <div class="sec-head"><h2>Install on a device</h2></div>
        <div class="stack-sm">
          <p>Set the device to use this proxy, then open this address in its browser. The page offers the certificate, instructions for the platform and a check of whether it is trusted.</p>
          <div class="row tight">
            <code class="grow mono url">{{ ca.page_url }}</code>
            <PagesCopyButton :value="ca.page_url" />
          </div>
          <p class="muted small">
            The address <code>{{ ca.magic_host }}</code> only exists through the proxy: it is answered by MidoPress and never forwarded, so it does not open from a browser that is not using it.
          </p>
        </div>
      </section>

      <section class="sec">
        <div class="sec-head"><h2>Download</h2></div>
        <div class="row tight">
          <button class="btn" @click="download('pem')">PEM (.crt)</button>
          <button class="btn" @click="download('der')">DER (.der)</button>
          <button class="btn" @click="download('p12')">PKCS#12 (.p12)</button>
        </div>
        <p class="muted small formats">
          PEM is what most systems and Firefox want, DER is the binary form (Windows, Java). The PKCS#12 file is a trust store holding only the certificate, protected with the password <code>midopress</code>. The private key is never downloadable.
        </p>
      </section>

      <section class="sec">
        <div class="sec-head"><h2>Platform steps</h2></div>
        <div class="plat">
          <details v-for="p in platforms" :key="p.name">
            <summary>{{ p.name }}</summary>
            <ol>
              <li v-for="s in p.steps" :key="s">{{ s }}</li>
            </ol>
            <div v-if="p.command" class="cmd row tight">
              <code class="grow mono">{{ p.command }}</code>
              <PagesCopyButton :value="p.command" />
            </div>
          </details>
        </div>
      </section>

      <section class="sec">
        <div class="sec-head"><h2>What trusting it allows</h2></div>
        <div class="stack-sm">
          <p>A device that trusts this authority accepts any HTTPS certificate MidoPress signs with it. That is what lets the proxy see and recompress encrypted pages, and it means whoever holds the private key can impersonate any website to those devices.</p>
          <ul class="plain">
            <li>Install it only on devices you own or administer, and only on those that use this proxy.</li>
            <li>Never share <code>ca.key</code>, and keep it readable by the MidoPress user only. Anyone who gets it can intercept the traffic of every device that trusts the CA.</li>
            <li>If the key may have leaked, remove the certificate from the devices and create a new authority.</li>
            <li>Banking and other sensitive hosts can be excluded from decryption with <code>mitm.passthrough</code> in the configuration, or a rule with action <code>passthrough_tls</code>.</li>
          </ul>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped src="../components/PagesOps.css"></style>
<style scoped>
.fp code, .url { overflow-wrap: anywhere; }
.url { font-size: 1.05rem; padding: 0.4rem 0.6rem; user-select: all; }
.formats { margin-top: 0.6rem; max-width: 70ch; }
.steps { margin: 0; padding-left: 1.2rem; display: flex; flex-direction: column; gap: 0.8rem; }
.steps .pre { margin: 0.3rem 0; }
.plat { border: 1px solid var(--border); border-radius: 3px; }
.plat details + details { border-top: 1px solid var(--border); }
.plat summary { padding: 0.5rem 0.8rem; cursor: pointer; font-weight: 500; }
.plat ol { margin: 0; padding: 0 0.8rem 0.6rem 2.2rem; display: flex; flex-direction: column; gap: 0.3rem; }
.cmd { padding: 0 0.8rem 0.7rem; }
.cmd code { overflow-wrap: anywhere; padding: 0.3rem 0.5rem; }
.plain { margin: 0; padding-left: 1.2rem; display: flex; flex-direction: column; gap: 0.3rem; }
</style>
