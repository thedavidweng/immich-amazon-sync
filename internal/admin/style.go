package admin

const stylesheet = `
@font-face{font-family:"Cal Sans";font-style:normal;font-weight:400;font-display:swap;src:url(/calsans.woff2) format("woff2")}
:root{color-scheme:light dark;--page:#fafafa;--tray:#f4f4f5;--sheet:#fff;--line:#e6e6e8;--line-soft:#efeff1;--fg:#202023;--fg-2:#3f3f46;--muted:#71717a;--faint:#a1a1aa;--hover:rgba(0,0,0,.04);--primary:#202023;--on-primary:#fff;--ring:rgba(32,32,35,.14);--ok:#16a34a;--info:#2563eb;--warn:#d97706;--bad:#dc2626;--bad-text:#b91c1c;--bad-line:#fbd5d5;--seg-done:#202023;--seg-existing:#a1a1aa;--seg-queued:#dcdce0;--sans:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;--display:"Cal Sans",var(--sans);--mono:ui-monospace,"SF Mono",SFMono-Regular,Menlo,Consolas,monospace;--shadow-xs:0 1px 2px rgba(0,0,0,.05)}
@media(prefers-color-scheme:dark){:root{--page:#0c0c0d;--tray:#131315;--sheet:#1a1a1d;--line:#2a2a2e;--line-soft:#232327;--fg:#ececee;--fg-2:#d4d4d8;--muted:#9a9aa3;--faint:#62626a;--hover:rgba(255,255,255,.05);--primary:#ececee;--on-primary:#18181b;--ring:rgba(236,236,238,.18);--ok:#22c55e;--info:#60a5fa;--warn:#f59e0b;--bad:#f87171;--bad-text:#fca5a5;--bad-line:#4a2323;--seg-done:#ececee;--seg-existing:#6b6b73;--seg-queued:#36363b;--shadow-xs:0 1px 2px rgba(0,0,0,.4)}}
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--page);color:var(--fg);font:14px/1.5 var(--sans);-webkit-font-smoothing:antialiased}
main{max-width:960px;margin:0 auto;padding:40px 24px 56px}
.center{min-height:100vh;display:grid;place-items:center}.narrow{width:100%;max-width:400px;padding:32px 16px}.editor{max-width:720px}
h1,h2{margin:0;font-family:var(--display);font-weight:400;letter-spacing:-.01em;line-height:1.2;text-wrap:balance}
h1{font-size:28px}h2{font-size:20px}
h3{margin:16px 0 4px;font-size:12px;font-weight:500;color:var(--muted)}
p{margin:0;max-width:68ch;text-wrap:pretty}
a{color:var(--fg);text-decoration:underline;text-decoration-color:var(--faint);text-underline-offset:3px;overflow-wrap:anywhere}a:hover{text-decoration-color:currentColor}
code{font-family:var(--mono);font-size:12px}
time,dd,.legend{font-variant-numeric:tabular-nums}
::selection{background:var(--ring);color:var(--fg)}
:focus-visible{outline:2px solid var(--fg);outline-offset:2px}

.topbar{border-bottom:1px solid var(--line)}
.bar{max-width:960px;margin:0 auto;padding:0 24px;height:56px;display:flex;align-items:center;justify-content:space-between;gap:16px}
.bar-actions{display:flex;align-items:center;gap:6px}
.brand{font-family:var(--display);font-size:18px;letter-spacing:-.01em;word-spacing:.1em;color:var(--fg);text-decoration:none;white-space:nowrap}
.brand span{color:var(--muted)}
.narrow>.brand{display:inline-block;margin:0 0 20px 4px}

button,.button{font:inherit;font-weight:500;font-size:14px;display:inline-flex;align-items:center;justify-content:center;gap:6px;height:36px;padding:0 14px;border-radius:8px;border:1px solid var(--primary);background:var(--primary);color:var(--on-primary);box-shadow:inset 0 1px 0 rgba(255,255,255,.12),var(--shadow-xs);cursor:pointer;white-space:nowrap;text-decoration:none;transition:background-color .15s,border-color .15s,box-shadow .15s,opacity .15s}
button:hover,.button:hover{opacity:.88}
button:active,.button:active{transform:translateY(.5px)}
.small{height:32px;padding:0 10px;font-size:13px;border-radius:7px}
.secondary{background:var(--sheet);color:var(--fg);border-color:var(--line);box-shadow:var(--shadow-xs)}
.ghost{background:transparent;color:var(--fg-2);border-color:transparent;box-shadow:none}
.secondary:hover,.ghost:hover{opacity:1;background:var(--hover)}
.secondary:hover{border-color:var(--faint)}
button.danger{background:var(--sheet);color:var(--bad-text);border-color:var(--bad-line);box-shadow:var(--shadow-xs)}button.danger:hover{opacity:1;border-color:var(--bad)}
button:disabled{opacity:.5;cursor:not-allowed}
@media(pointer:coarse){button,.button{height:44px}}

.page-title{margin-bottom:28px}.page-title p{margin-top:6px;color:var(--muted)}
.note{color:var(--muted);font-size:13px}
.notice{margin:0 0 20px;max-width:none;padding:12px 16px;border:1px solid var(--line);border-radius:10px;background:var(--sheet);color:var(--fg-2);font-size:13px}
footer{margin-top:28px;font-size:12px;color:var(--muted)}

.frame,.account{background:var(--tray);border:1px solid var(--line);border-radius:16px;padding:4px;margin-bottom:20px}
.frame-head{padding:14px 16px 16px}.frame-head p{margin-top:4px;color:var(--muted);font-size:13px}
.sheet{background:var(--sheet);border:1px solid var(--line);border-radius:12px;padding:20px;box-shadow:var(--shadow-xs)}
.account-head{display:flex;align-items:center;justify-content:space-between;gap:12px 24px;flex-wrap:wrap}
.identity{min-width:0}
.toolbar{display:flex;flex-wrap:wrap;gap:2px}

.health{display:flex;align-items:center;justify-content:space-between;gap:8px 16px;flex-wrap:wrap}
.since{color:var(--muted);font-size:13px}
.badge{display:inline-flex;align-items:center;gap:7px;height:26px;padding:0 10px;border:1px solid var(--line);border-radius:999px;font-size:13px;font-weight:500;color:var(--fg);background:var(--sheet);overflow-wrap:anywhere}
.badge:before{content:"";width:7px;height:7px;border-radius:50%;background:var(--muted);flex-shrink:0}
.badge.ok:before{background:var(--ok)}.badge.info:before{background:var(--info)}.badge.warn:before{background:var(--warn)}.badge.bad:before{background:var(--bad)}
.badge.bad{color:var(--bad-text);border-color:var(--bad-line)}

.alert{display:flex;gap:10px;align-items:flex-start;margin:16px 0 0;max-width:none;padding:12px 14px;border:1px solid var(--bad-line);border-radius:10px;color:var(--fg-2);font-size:13px}
.alert svg{color:var(--bad);flex-shrink:0;margin-top:2px}
.alert strong{font-weight:500;color:var(--bad-text)}
.codes{display:grid;gap:2px;margin-top:4px}.codes div{display:flex;gap:16px}.codes dd{margin:0}

.distribution{display:block;margin-top:24px;border-radius:4px;background:var(--line-soft)}
.distribution rect{stroke:var(--sheet);stroke-width:1.5px}
.done{fill:var(--seg-done);background:var(--seg-done)}.existing{fill:var(--seg-existing);background:var(--seg-existing)}.queued{fill:var(--seg-queued);background:var(--seg-queued)}.verifying{fill:var(--warn);background:var(--warn)}.attention{fill:var(--bad);background:var(--bad)}
.legend{list-style:none;display:flex;flex-wrap:wrap;gap:6px 20px;margin:12px 0 0;padding:0;font-size:13px;color:var(--muted)}
.legend li{display:inline-flex;align-items:center;gap:7px}
.legend i{width:8px;height:8px;border-radius:2px}
.legend b{font-weight:500;color:var(--fg)}
.empty-queue{margin-top:20px;color:var(--muted)}

dl{margin:0}dd{margin:0}
.facts{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px 24px;margin-top:24px;padding-top:20px;border-top:1px solid var(--line-soft)}
.facts dt{font-size:12px;color:var(--muted)}
.facts dd{margin-top:2px;font-size:13px}

.accordion{margin-top:4px}
.accordion details{border-top:1px solid var(--line-soft);padding:0 16px}
.accordion details:first-child{border-top:0}
summary{display:flex;align-items:center;gap:12px;min-height:44px;cursor:pointer;list-style:none;font-weight:500;color:var(--fg)}
summary::-webkit-details-marker{display:none}
summary span{margin-left:auto;font-weight:400;font-size:13px;color:var(--muted)}
.chevron{flex-shrink:0;color:var(--muted);transition:transform .2s cubic-bezier(.16,1,.3,1)}
.danger-zone summary{color:var(--muted)}.danger-zone .chevron{margin-left:auto}
details[open]>summary .chevron{transform:rotate(180deg)}
summary:hover .chevron,summary:hover span{color:var(--fg)}
details[open]{padding-bottom:16px}
details>.note{margin-top:12px}details>form{margin-top:12px}
.rows{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));column-gap:32px;font-size:13px}
.rows div{display:flex;justify-content:space-between;align-items:baseline;gap:16px;padding:7px 0;border-bottom:1px solid var(--line-soft)}
.rows dt{color:var(--muted);min-width:0}
.rows dd{text-align:right}
.sheet.rows{padding:8px 20px}

.empty .sheet{padding:40px 24px;text-align:center}.empty p{margin:8px auto 20px;color:var(--muted)}

.frame>.sheet>h1{font-size:22px}.frame>.sheet>p{margin-top:8px;color:var(--muted)}.frame>.sheet>p+a{display:inline-block;margin-top:16px}
.stack{display:grid;gap:16px}
.narrow>.note{margin-top:16px;text-align:center}
.editor nav{margin-bottom:20px;font-size:13px}.editor>h1+p{margin:6px 0 28px;color:var(--muted)}
fieldset{background:var(--sheet);border:1px solid var(--line);border-radius:12px;margin:20px 0 0;padding:20px;min-width:0;box-shadow:var(--shadow-xs)}
legend{float:left;width:100%;padding:0;margin-bottom:16px;font-family:var(--display);font-size:17px;letter-spacing:-.01em}legend+*{clear:both}
label{display:block;font-size:13px;font-weight:500;color:var(--fg);overflow-wrap:anywhere}
input,textarea,select{font:inherit;color:var(--fg);accent-color:var(--primary)}
input:not([type=checkbox]),textarea,select{display:block;width:100%;margin-top:6px;padding:7px 11px;min-height:36px;background:var(--sheet);border:1px solid var(--line);border-radius:8px;font-weight:400;box-shadow:var(--shadow-xs);transition:border-color .15s,box-shadow .15s}
input:not([type=checkbox]):hover,textarea:hover,select:hover{border-color:var(--faint)}
input:not([type=checkbox]):focus,textarea:focus,select:focus{outline:0;border-color:var(--faint);box-shadow:0 0 0 3px var(--ring)}
input::placeholder,textarea::placeholder{color:var(--muted)}
input[readonly]{background:var(--tray);color:var(--muted);box-shadow:none}
textarea{resize:vertical;min-height:84px;font-family:var(--mono);font-size:12px;line-height:1.6}
.check{display:flex;align-items:flex-start;gap:10px;font-weight:400}
.check input{width:16px;height:16px;flex-shrink:0;margin:2px 0 0}
.check small{display:block;color:var(--muted)}
.stack>.note{margin-top:-8px}
label>.note{display:block;margin-top:6px;font-weight:400}
.actions{display:flex;flex-wrap:wrap;align-items:center;gap:8px 20px;margin-top:24px}.stack .actions{margin-top:0}
.album-list{max-height:22rem;overflow:auto;border:1px solid var(--line-soft);border-radius:10px;padding:8px 12px;scrollbar-width:thin;scrollbar-color:var(--line) transparent}
.album-list label{padding:6px 0}.album-list p{margin:0 0 4px;font-size:13px;color:var(--muted)}
.diagnostics{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);margin-top:20px;background:var(--sheet);border:1px solid var(--line);border-radius:12px;overflow:hidden;box-shadow:var(--shadow-xs)}
.diagnostics dt,.diagnostics dd{padding:10px 16px;border-bottom:1px solid var(--line-soft);overflow-wrap:anywhere}
.diagnostics dt{color:var(--muted)}
.diagnostics dt:last-of-type,.diagnostics dd:last-of-type{border-bottom:0}
.editor>p:not(.notice){color:var(--muted)}.editor>.notice{margin-top:20px}

@media(max-width:720px){main{padding:28px 16px 40px}.bar{padding:0 16px}h1{font-size:24px}.facts{grid-template-columns:repeat(2,minmax(0,1fr))}.rows{grid-template-columns:minmax(0,1fr)}.sheet{padding:16px}.frame-head{padding:12px 12px 14px}.accordion details{padding:0 12px}.toolbar{margin-left:-10px}.diagnostics{grid-template-columns:minmax(0,1fr)}.diagnostics dt{border:0;padding-bottom:0}.diagnostics dd{padding-top:2px}fieldset{padding:16px}}
@media(max-width:420px){.bar{gap:8px}.brand{font-size:15px}.bar-actions{gap:2px}.bar-actions .ghost{padding:0 6px}}
@media(max-width:340px){.facts{grid-template-columns:minmax(0,1fr)}}
@media(prefers-reduced-motion:reduce){*{transition:none!important}}
`
