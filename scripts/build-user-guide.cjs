// Render and verify a Persian A4 manual. HTML already embeds local fonts/screenshots.
const {chromium}=require('playwright');
const fs=require('node:fs');const path=require('node:path');const {execFileSync}=require('node:child_process');
const root=path.resolve(__dirname,'..'),html=path.join(root,'docs/user-guide-fa.html'),pdf=path.join(root,'docs/keepet-user-guide-fa.pdf');
(async()=>{const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',args:['--no-sandbox']});const p=await browser.newPage();await p.setContent(fs.readFileSync(html,'utf8'),{waitUntil:'load'});await p.evaluate(()=>document.fonts.ready);await p.waitForFunction(()=>Array.from(document.images).every(i=>i.complete&&i.naturalWidth>0));const footer=`<div style="width:100%;font-family:Arial,sans-serif;font-size:8px;color:#738077;padding:0 15mm;display:flex;justify-content:space-between"><span>KEEPET · User Guide · 2026-10-14</span><span><span class="pageNumber"></span> / <span class="totalPages"></span></span></div>`;const opts={path:pdf,format:'A4',printBackground:true,preferCSSPageSize:true,displayHeaderFooter:true,headerTemplate:'<span></span>',footerTemplate:footer,tagged:true,outline:true};await p.pdf(opts);
// Chromium produces actual named destinations for the TOC. Resolve their page numbers.
const numbers=JSON.parse(execFileSync('python3',['-c',`import fitz,json
d=fitz.open(${JSON.stringify(pdf)})
data=json.load(open(${JSON.stringify(path.join(root,'docs/user-guide-fa.content.json'))}))
targets=[p['id'] for p in data['pages']];out={}
for pg in range(1,5):
 ls=[l for l in d[pg].get_links() if 'page' in l and l['page']>=0]
 expected=targets[(pg-1)*16:pg*16]+['directory']
 assert len(ls)==len(expected),(pg,len(ls),len(expected))
 for key,l in zip(expected,ls):out[key]=l['page']+1
print(json.dumps(out))`],{encoding:'utf8'}));
await p.evaluate(numbers=>{const fa=n=>String(n).replace(/\d/g,d=>'۰۱۲۳۴۵۶۷۸۹'[d]);document.querySelectorAll('[data-target]').forEach(el=>{if(numbers[el.dataset.target])el.textContent='ص '+fa(numbers[el.dataset.target])})},numbers);fs.writeFileSync(html,await p.content());await p.pdf(opts);await browser.close();console.log(pdf);})().catch(e=>{console.error(e);process.exit(1)});
