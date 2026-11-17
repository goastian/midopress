# Generated integration fixtures

`cmyk-lut.jpg` is a synthetic 192×128 CMYK image with the generated CMYK LUT ICC
profile from `internal/colorcms/testdata`. It contains no third-party photograph.
Regenerate with `internal/colorcms/wasm/generate_cmyk_jpeg.c`, a C compiler and
libjpeg. The integration test compares against independently decoded CMYK pixels
transformed with the embedded CMS; it requires actual modern recoding.

The AVIF integration fixture is generated during the test from a synthetic
picture. Its metadata helper attaches primary-item ICC/crop/rotation/mirror
properties and relocates media offsets without changing coded pixels.
