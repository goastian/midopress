// A fixed development CA certificate (self-signed, EC P-256, the key was thrown
// away) and its PKCS#12 trust store (password "midopress"), both made with
// openssl. Node cannot create certificates without dependencies, so they are
// embedded. They are for testing only.

import { X509Certificate } from 'node:crypto'

export const CA_PEM = `
-----BEGIN CERTIFICATE-----
MIIBwjCCAWmgAwIBAgIUB5+ba81MDEqteYwYqpG5d1fMR5cwCgYIKoZIzj0EAwIw
NzESMBAGA1UECgwJTWlkb1ByZXNzMSEwHwYDVQQDDBhNaWRvUHJlc3MgRGV2ZWxv
cG1lbnQgQ0EwHhcNMjYxMDIyMDA0ODU0WhcNMzYxMDE5MDA0ODU0WjA3MRIwEAYD
VQQKDAlNaWRvUHJlc3MxITAfBgNVBAMMGE1pZG9QcmVzcyBEZXZlbG9wbWVudCBD
QTBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABKe4WZZB1C88jIx6IMZ9Z6UMRNah
5Ipn8jh3LpJVNdwLB92JwWOB8uQ1K5C5yJdoFiL7RlMjr++rHA1CarHJGDmjUzBR
MB0GA1UdDgQWBBTTkvols7JgNIkBetCgsSQThtrgJzAfBgNVHSMEGDAWgBTTkvol
s7JgNIkBetCgsSQThtrgJzAPBgNVHRMBAf8EBTADAQH/MAoGCCqGSM49BAMCA0cA
MEQCIBZOv5z2FrGtWg+O6duUReiVsjOkTmfM6Q0eYSZTrDotAiAeSjvM+NVWvCmn
YhzhjIk/Hn/dUghOiowrs2QYBwCgLQ==
-----END CERTIFICATE-----
`.trimStart()

export const CA_P12 = Buffer.from(
  `
MIIC7wIBAzCCAqUGCSqGSIb3DQEHAaCCApYEggKSMIICjjCCAooGCSqGSIb3DQEHBqCCAnswggJ3
AgEAMIICcAYJKoZIhvcNAQcBMF8GCSqGSIb3DQEFDTBSMDEGCSqGSIb3DQEFDDAkBBCQYs4Ywp9P
vMWgOdKO2fOLAgIIADAMBggqhkiG9w0CCQUAMB0GCWCGSAFlAwQBKgQQl+ODRd7gny1FYWSUJ5nx
HoCCAgA63AS5vjqaPcUXckr++bwO6od9ZNwasCc+kh2YjkVeQnW3pA76d/JNBhwwepzFWuydapfM
SEX1NNZUTk3YsovFTTlDi2SUCbU+4L+dVk+QfP+j2nHJlj/MPdFZNsBnrsERypZ2pLyrKtgEMegc
Z2VX2j7DJzC5RxGnEAu90M5B70Mry15/fxAWrWd8edfZVVGWVgDriPlXLLe3bPQFZiPXTXw3GNCm
X9nnq+jE8fFaUHU12NvVJfOZ/OE8nDiClJABgC8G9ZtO85ldINzZPTkeqx4JxQ4pCK288xYjEyX3
QZUitdDvK4Ay334h4dUnM0UQlS6pzQZ663LooUpbQ69ulqiQhVOqKUn2UCV0fywdQwAXnPetWEMx
I+0Q9yGWLldYfhkrcS5rgdWYYsXEyDEmXoOGlyy19w19cKSKE4QiaC591n8L7j9x/8UmAlDx6nr9
YwJ9fJKSgZ1rbZK4QhFseznWYOIJgokFnNtI2VKzN580RyTfO/48q9tyPOOCjIALLuSvT/tuuZ/n
fqdBMNJJYWSupxq25Eqh/iT115EW7tUA78wxqyJHmafNRR8g8Alkr8Oo1+22GACW16OBcpWlDIZu
mNQBdsDy3dy0jqprb0TjmwlKl5k1+98EGA1X33YI3Erah4pROzt/lymw4h8/UbnO+VCox3waFu0Q
uojLWDBBMDEwDQYJYIZIAWUDBAIBBQAEIBR8/NOD2My/swkl6juTiNTFx4zkr4zgftjXLR3PM7pr
BAgzGOZzhqTZzgICCAA=
`.replace(/\s+/g, ''),
  'base64',
)

const x509 = new X509Certificate(CA_PEM)
export const CA_DER = x509.raw

export function caInfo() {
  const subject = x509.subject.split('\n').reverse().join(', ')
  return {
    available: true,
    subject,
    not_before: new Date(x509.validFrom).toISOString(),
    not_after: new Date(x509.validTo).toISOString(),
    sha256: x509.fingerprint256.replaceAll(':', '').toLowerCase(),
    page_url: 'http://midopress.it/',
    magic_host: 'midopress.it',
  }
}
