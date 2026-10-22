package magic

const pageCSS = `
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:#f2f8f4;color:#12261d;font:16px/1.5 system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif}
a{color:#0f6b3f}
.wrap{max-width:46rem;margin:0 auto;padding:0 1rem}
.top{background:#0f3d2e;color:#fff;padding:1.25rem 0 1.5rem}
.top h1{margin:.25rem 0 .5rem;font-size:1.6rem;line-height:1.2}
.top .lead{margin:0;color:#d4eadd}
.brand{margin:0;font-weight:700;letter-spacing:.04em;color:#7fdca9}
.card .brand{color:#1f9a5c}
.card{background:#fff;border:1px solid #d3e5da;border-radius:14px;padding:1rem 1.1rem;margin:1rem 0}
h2{margin:0 0 .6rem;font-size:1.2rem;color:#0f3d2e}
.status{border-radius:10px;padding:.7rem .9rem;background:#eef3f0;border:1px solid #d3e5da;font-weight:600}
.status p{margin:0}
.status[data-state=ok]{background:#dff5e8;border-color:#1f9a5c;color:#0b4a2a}
.status[data-state=no]{background:#fff4dc;border-color:#d99a1c;color:#5c3d00}
.status[data-state=checking]{color:#3a5246}
.btn{display:block;text-align:center;background:#1f9a5c;color:#fff;font-weight:700;font-size:1.15rem;text-decoration:none;padding:.9rem 1rem;border-radius:12px}
.btn:hover,.btn:focus{background:#178049}
a:focus,summary:focus{outline:3px solid #f2b300;outline-offset:2px}
.alt,.small{font-size:.9rem;color:#42584c}
.qrbox{display:flex;flex-wrap:wrap;align-items:center;gap:1rem;margin-top:1rem}
.qr{width:9.5rem;height:9.5rem;color:#0f3d2e;background:#fff;border-radius:8px;flex:none}
.qr svg{display:block;width:100%;height:100%}
.qrbox .small{flex:1;min-width:12rem;margin:0}
.plat{border:1px solid #d3e5da;border-radius:12px;margin:.6rem 0;background:#fafdfb}
.plat summary{cursor:pointer;font-weight:700;padding:.8rem 1rem;color:#0f3d2e}
.plat[open] summary{border-bottom:1px solid #d3e5da}
.plat .body{padding:.2rem 1rem 1rem}
.plat ol{padding-left:1.3rem}
.plat li{margin:.5rem 0}
pre{margin:.4rem 0 0;padding:.6rem .7rem;background:#0f3d2e;color:#e6f6ec;border-radius:8px;overflow-x:auto;font-size:.85rem;line-height:1.4}
code,.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
.note{background:#eef6f1;border-radius:8px;padding:.6rem .8rem;font-size:.95rem}
.undo{font-size:.95rem;color:#42584c}
dl{margin:0}
dt{font-weight:700;margin-top:.5rem;font-size:.9rem;color:#42584c}
dd{margin:0;overflow-wrap:anywhere}
.mono{font-size:.85rem}
.foot{padding-bottom:2rem;font-size:.9rem;color:#42584c}
@media (prefers-color-scheme:dark){
body{background:#0a1a13;color:#e2f0e8}
a{color:#7fdca9}
.top{background:#0f3d2e}
.card{background:#10271c;border-color:#1e4a35}
h2,.plat summary{color:#c9eedb}
.card .brand{color:#4fd08a}
.status{background:#16301f;border-color:#1e4a35}
.status[data-state=ok]{background:#0f3d24;border-color:#2fbf78;color:#c9f5dc}
.status[data-state=no]{background:#3b2d0a;border-color:#d99a1c;color:#ffe6ae}
.status[data-state=checking]{color:#b9d3c4}
.btn{background:#2fbf78;color:#04170e}
.btn:hover,.btn:focus{background:#4fd08a}
.alt,.small,.undo,dt,.foot{color:#a9c4b5}
.qr{background:#fff;color:#0f3d2e}
.plat{background:#0d2218;border-color:#1e4a35}
.plat[open] summary{border-color:#1e4a35}
.note{background:#16301f}
pre{background:#04120b;color:#e6f6ec}
}
@media (min-width:40rem){
.btn{display:inline-block;min-width:22rem}
.top h1{font-size:2rem}
}
`
