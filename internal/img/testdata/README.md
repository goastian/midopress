Fixtures of the WebP decoding test.

`opaque.webp` and `alpha.webp` are 77x65 and 97x65 crops of a Kodak image (made
available by Kodak for unrestricted use, https://r0k.us/graphics/kodak/), encoded
with lossy WebP at quality 80. The `.ref.png` files are what libwebp's `dwebp`
(libwebp 1.5, Debian trixie) makes of them, which is what a browser shows.
