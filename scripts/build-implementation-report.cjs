// Print a self-contained Persian report and reject content that would spill off a page.
const {chromium}=require('playwright');
const fs=require('node:fs');const path=require('node:path');const {execFileSync}=require('node:child_process');
const root=path.resolve(__dirname,'..'),dir=path.join(root,'docs');
const manifest=JSON.parse(fs.readFileSync(path.join(dir,'implementation-report-fa.manifest.json'),'utf8'));
(async()=>{const browser=await chromium.launch({executablePath:process.env.CHROMIUM_PATH||'/usr/bin/chromium',args:['--no-sandbox']});try{
 const p=await browser.newPage({viewport:{width:1100,height:1250}});
 await p.setContent(fs.readFileSync(path.join(dir,'keepet-implementation-report-fa.html'),'utf8'),{waitUntil:'load'});
 await p.evaluate(()=>document.fonts.ready);
 const issues=await p.evaluate(()=>[...document.querySelectorAll('.sheet')].flatMap(el=>{
  const footer=el.querySelector('.footer').getBoundingClientRect(),body=el.querySelector('.body').getBoundingClientRect();
  return el.scrollHeight>el.clientHeight+2||body.bottom>footer.top-3?[{page:el.id,height:el.clientHeight,scroll:el.scrollHeight,bodyBottom:body.bottom,footerTop:footer.top}]:[];
 }));
 if(issues.length)throw Error('Page overflow: '+JSON.stringify(issues));
 await p.locator('#cover').screenshot({path:'/tmp/keepet-report-cover.png'});
 await p.locator('#wallet').screenshot({path:'/tmp/keepet-report-wallet.png'});
 await p.locator('#rollback').screenshot({path:'/tmp/keepet-report-rollback.png'});
 const pdf=path.join(dir,'keepet-implementation-report-fa.pdf');
 await p.pdf({path:pdf,format:'A4',preferCSSPageSize:true,printBackground:true,displayHeaderFooter:false,tagged:true,outline:true});
 const result=execFileSync('python3',['-c',`
import fitz,json,os
pdf=${JSON.stringify(pdf)}
manifest=json.load(open(${JSON.stringify(path.join(dir,'implementation-report-fa.manifest.json'))}))
d=fitz.open(pdf)
assert len(d)==manifest['page_count'],(len(d),manifest['page_count'])
for i,p in enumerate(d):
 assert len(p.get_text().strip())>150,(i,'blank/sparse page')
 assert abs(p.rect.width-595.276)<1 and abs(p.rect.height-841.89)<1,(i,p.rect)
 assert 'KEEPET' in p.get_text(),(i,'brand missing')
links=[l for p in d for l in p.get_links()]
external=[l for l in links if l.get('uri')]
internal=[l for l in links if l.get('page',-1)>=0 and not l.get('uri')]
assert len(external)>45,len(external)
assert len(internal)>=18,len(internal)
fonts={f[0] for p in d for f in p.get_fonts()}
assert any(d.extract_font(x)[3] for x in fonts),'fonts not embedded'
# Make contents-page links explicit page-top destinations across PDF readers.
targets={p['id']:i for i,p in enumerate(manifest['pages'])}
for pg in d:
 for link in pg.get_links():
  if link.get('nameddest') in targets:
   pg.delete_link(link)
   pg.insert_link({'kind':fitz.LINK_GOTO,'from':link['from'],'page':targets[link['nameddest']],'to':fitz.Point(0,0)})
d.set_metadata({'title':'گزارش پیاده‌سازی اصلاحات کیپت','author':'Keepet','subject':'امکانات محصول، مدیریت، تست، نصب و بازگشت؛ وضعیت کد تا fb51cea','keywords':'Keepet, implementation, Persian, release, rollback','creator':'Keepet report generator'})
d.set_toc([[1,p['title'],i+1] for i,p in enumerate(manifest['pages'])])
tmp=pdf+'.tmp';d.save(tmp,garbage=4,deflate=True);d.close();os.replace(tmp,pdf)
d=fitz.open(pdf)
for n,name in [(0,'cover'),(12,'content'),(17,'rollback')]:
 if n<len(d):d[n].get_pixmap(matrix=fitz.Matrix(1.15,1.15)).save('/tmp/keepet-report-pdf-'+name+'.png')
print(json.dumps({'pages':len(d),'external_links':len(external),'internal_links':len(internal),'embedded_font_resources':len(fonts),'bytes':os.path.getsize(pdf)},ensure_ascii=False))
`],{encoding:'utf8'});
 console.log(result.trim());console.log(pdf);
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});
