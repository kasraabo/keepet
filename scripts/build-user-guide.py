#!/usr/bin/env python3
"""Build the Persian manual HTML from audited content. Print with build-user-guide.cjs."""
from pathlib import Path
import base64,html,json,re
ROOT=Path(__file__).resolve().parent.parent
D=json.loads((ROOT/'docs/user-guide-fa.content.json').read_text())
BASE=D['base_url']; PAGES=D['pages']; OUT=ROOT/'docs/user-guide-fa.html'
def esc(t):
 text=html.escape(str(t))
 return re.sub(r'`([^`]+)`',r'<code>\1</code>',text)
def fa(n): return str(n).translate(str.maketrans('0123456789','۰۱۲۳۴۵۶۷۸۹'))
def uri(path): return path if path.startswith('https://') or path.startswith('http://') else BASE+path
def data(path,mime):return 'data:'+mime+';base64,'+base64.b64encode(path.read_bytes()).decode()
def route(name,path):
 value=uri(path)
 return '<div class="route"><span>'+esc(name)+'</span><a href="'+html.escape(value,quote=True)+'" dir="ltr">'+esc(value)+'</a></div>'
def table(t):
 if not t:return ''
 s='<table><thead><tr>'+''.join('<th>'+esc(x)+'</th>' for x in t['headers'])+'</tr></thead><tbody>'
 for row in t['rows']:
  s+='<tr>'+''.join('<td'+(' class="ltr"' if re.match(r'^(09[0-9]{9}|/[a-zA-Z])',str(x)) else '')+'>'+esc(x)+'</td>' for x in row)+'</tr>'
 return s+'</tbody></table>'
css='''
@font-face{font-family:Vazir;src:url(FONT_REG) format('woff2');font-weight:400}
@font-face{font-family:Vazir;src:url(FONT_BOLD) format('woff2');font-weight:700}
@page{size:A4;margin:15mm 15mm 17mm}
*{box-sizing:border-box}html,body{margin:0;padding:0}body{font-family:Vazir,sans-serif;font-size:9.7pt;line-height:1.62;color:#263c35;direction:rtl;-webkit-print-color-adjust:exact;print-color-adjust:exact}
.page{break-before:page;page-break-before:always;position:relative}.page:first-child{break-before:auto}p{margin:0 0 8px;orphans:3;widows:3}h1{font-size:26pt;line-height:1.5;margin:16px 0;color:#153e35}h2{font-size:18pt;line-height:1.4;color:#153e35;margin:5px 0 12px;break-after:avoid}h3{font-size:12pt;margin:12px 0 6px;break-after:avoid}.eyebrow{font-size:9pt;color:#6d8178;font-weight:700;margin-bottom:5px}.section-no{font-size:9pt;color:#547165;letter-spacing:.2px;border-bottom:1px solid #dce5df;padding-bottom:7px;margin-bottom:12px}.role{background:#edf4ef;border-radius:7px;padding:7px 10px;font-size:9pt;margin:7px 0 10px}.route-box{background:#f7f9f7;border-right:3px solid #b5cbbf;padding:7px 10px;margin-bottom:10px;break-inside:avoid}.route{margin:3px 0}.route span{display:block;font-size:8.4pt;font-weight:700}.route a{display:block;direction:ltr;text-align:left;font-size:8.5pt;unicode-bidi:isolate;overflow-wrap:anywhere;color:#1b6651;text-decoration:none;line-height:1.45}a{color:#1b6651;text-decoration:none}ol{margin:9px 0 12px;padding:0 20px 0 0}li{padding-right:4px;margin:0 0 6px;break-inside:avoid;orphans:2;widows:2}li::marker{color:#38765c;font-weight:700}.note{background:#fbf6ea;border-right:3px solid #bc9c4b;border-radius:5px;padding:8px 10px;font-size:9pt;margin-top:10px;break-inside:avoid}.note p:last-child{margin-bottom:0}.outcome{border-top:1px solid #d6e4da;margin-top:13px;padding-top:8px;font-size:9pt;break-inside:avoid}.outcome b{color:#246e4e}.image{margin:14px 0 9px;break-inside:avoid}.image img{display:block;width:100%;max-height:55mm;object-fit:contain;border:1px solid #d8e1dc;border-radius:7px}.image figcaption{font-size:8pt;color:#728078;margin-top:4px}.intro{font-size:10.3pt;color:#52645b;margin-bottom:12px}.back{font-size:8.2pt;margin-top:14px;color:#71877a}.ltr,code,pre{direction:ltr;unicode-bidi:isolate;text-align:left}code{font-family:Vazir,monospace;font-size:9pt}pre{white-space:pre-wrap;overflow-wrap:anywhere;word-break:break-word;border-radius:5px;background:#edf1ee;padding:10px;font-family:monospace;font-size:8.2pt;line-height:1.6;margin:5px 0 10px;break-inside:avoid}table{border-collapse:collapse;width:100%;font-size:9pt;line-height:1.6;margin:10px 0 13px}th{background:#e7efe9;font-weight:700;color:#174632;text-align:right}th,td{border:1px solid #dae3dd;padding:7px 8px;vertical-align:top;overflow-wrap:anywhere}tr{break-inside:avoid}thead{display:table-header-group}td.ltr{white-space:nowrap;font-size:8.8pt}.toc-item{display:flex;justify-content:space-between;gap:15px;align-items:center;border-bottom:1px solid #e5ebe7;padding:8px 0;font-size:10pt;break-inside:avoid}.toc-item .number{color:#728579;min-width:33px;text-align:left;font-size:9pt}.toc-label{display:flex;gap:10px}.toc-label .num{color:#87a194}.cover{min-height:239mm;border:1px solid #d6e3db;border-radius:16px;padding:25mm 16mm;display:flex;flex-direction:column;justify-content:space-between;background:linear-gradient(140deg,#f7faf7,#edf3ec)}.cover-logo{display:flex;align-items:center;gap:13px}.cover-logo img{width:56px;height:56px}.cover .k{font-size:28pt;font-weight:700;color:#153e35}.cover .subtitle{font-size:17pt;font-weight:700;margin:12px 0;color:#54735c}.cover .caption{font-size:11pt;line-height:2;color:#667c6d}.cover .pill{display:inline-block;border:1px solid #b8cebd;border-radius:20px;padding:5px 13px;font-size:9pt}.cover .meta{font-size:10pt;line-height:2.2}.cover a{direction:ltr;unicode-bidi:isolate;display:inline-block}.directory .route-box{margin-bottom:9px}.dir-intro{font-size:9.5pt}.check{font-size:9pt;color:#60736a}
'''
css+='\n#public .route-box{display:grid;grid-template-columns:1fr 1fr;gap:3px 12px}#professional .image img{max-height:43mm}#availability .image img{max-height:32mm}#public td,#public th{padding:5px 8px}\n'
css=css.replace('FONT_REG',data(ROOT/'web/static/vendor/Vazirmatn-Regular.woff2','font/woff2')).replace('FONT_BOLD',data(ROOT/'web/static/vendor/Vazirmatn-Bold.woff2','font/woff2'))
parts=['<!doctype html><html lang="fa" dir="rtl"><head><meta charset="utf-8"><title>راهنمای جامع کار با کیپت</title><style>'+css+'</style></head><body>']
icon=data(ROOT/'web/static/icon.svg','image/svg+xml')
parts.append('<section class="page cover" id="cover"><div><div class="cover-logo"><img src="'+icon+'"><span class="k">کیپت</span></div><div class="pill">دفترچهٔ عملیاتی · نسخهٔ آزمایشی</div><h1>راهنمای جامع<br>کار با تمام بخش‌های سایت</h1><div class="subtitle">از اولین ورود تا مدیریت پلتفرم</div><p class="caption">آموزش قدم‌به‌قدم مشتری، دامپزشک، گرومر، نمونه‌بردار، عملیات، مدیر و تحریریه؛ با لینک‌های قابل کلیک و تمام حساب‌های تست.</p></div><div class="meta"><div>تاریخ تهیه: '+esc(D['date'])+' · ۱۴ اکتبر ۲۰۲۶</div><div>نسخهٔ مبنا: <code>'+esc(D['version'])+'</code></div><div>نشانی آموزش: <a href="'+BASE+'">'+BASE+'</a></div><div>'+fa(len(PAGES))+' بخش آموزشی + فهرست لینک‌ها + سناریوهای QC</div><div class="check">مبنای راهنما: کد و صفحه‌های نسخهٔ بررسی‌شده؛ وضعیت نصب روی سرور کاربر جداگانه بررسی می‌شود.</div></div></section>')
# Reserve four clean contents pages; page numbers filled after the first PDF render.
for start in range(0,len(PAGES),16):
 parts.append('<section class="page toc" id="toc-'+str(start//16)+'"><div class="eyebrow">راهنمای کلیک‌پذیر</div><h2>فهرست بخش‌ها'+(' · ادامه' if start else '')+'</h2><p>روی عنوان بزنید تا به بخش مربوط بروید. لینک سایت در ابتدای همان بخش آمده است.</p>')
 for n,p in enumerate(PAGES[start:start+16],start+1):
  parts.append('<a class="toc-item" href="#'+p['id']+'"><span class="toc-label"><span class="num">'+fa(n)+'</span><span>'+esc(p['title'])+'</span></span><span class="number" data-target="'+p['id']+'">—</span></a>')
 parts.append('<a class="toc-item" href="#directory"><span>فهرست یک‌جای لینک‌های سایت و اسناد</span><span class="number" data-target="directory">—</span></a></section>')
for n,p in enumerate(PAGES,1):
 parts.append('<section class="page" id="'+p['id']+'"><div class="section-no">بخش '+fa(n)+' / '+fa(len(PAGES))+' · کیپت؛ آموزش کاربردی</div><h2>'+esc(p['title'])+'</h2><div class="role"><b>حساب / نقش لازم:</b> '+esc(p['role'])+'</div><div class="route-box">'+''.join(route(*link) for link in p['links'])+'</div>')
 if p['intro']:parts.append('<p class="intro">'+esc(p['intro'])+'</p>')
 parts.append(table(p.get('table')))
 if p['steps']:parts.append('<ol>'+''.join('<li>'+esc(x)+'</li>' for x in p['steps'])+'</ol>')
 for title,body in p.get('extra',[]):parts.append('<h3>'+esc(title)+'</h3><pre>'+html.escape(body)+'</pre>')
 if p.get('image'):
  img=ROOT/'docs/manual-assets'/f"{p['image']}.png"
  if not img.exists():raise FileNotFoundError(img)
  parts.append('<figure class="image"><img src="'+data(img,'image/png')+'"><figcaption>نمونهٔ آموزشی از محیط QA محلی؛ داده و تعداد کارت‌ها ممکن است با سایت شما متفاوت باشد.</figcaption></figure>')
 if p['notes']:parts.append('<aside class="note"><b>نکتهٔ کاربردی</b>'+''.join('<p>'+esc(x)+'</p>' for x in p['notes'])+'</aside>')
 if p['outcome']:parts.append('<div class="outcome"><b>نتیجهٔ مورد انتظار:</b> '+esc(p['outcome'])+'</div>')
 parts.append('<div class="back"><a href="#toc-0">بازگشت به فهرست ←</a></div></section>')
# Every stable path used by the manual, plus all public informational pages and support docs.
links={}
for p in PAGES:
 for label,path in p['links']:
  if path not in links:links[path]=label
for slug,label in [('about','دربارهٔ کیپت'),('terms','قوانین'),('privacy','حریم خصوصی'),('refund','بازگشت وجه'),('telemedicine','محدودیت مشاوره'),('contact','تماس'),('careers','فرصت همکاری'),('referral','معرفی دوستان'),('affiliate','همکاری در فروش')]: links['/pages/'+slug]=label
for path,label in [('https://github.com/kasraabo/keepet/blob/main/docs/product-configuration.md','جزئیات امکانات جدید'),('https://github.com/kasraabo/keepet/blob/main/docs/consultation-chat.md','سند گفتگو'),('https://github.com/kasraabo/keepet/blob/main/docs/availability.md','سند زمان‌های کاری'),('https://github.com/kasraabo/keepet/blob/main/docs/platform-management.md','سند مدیریت'),('https://github.com/kasraabo/keepet/blob/main/docs/deployment.md','سند اجرا'),('https://github.com/kasraabo/keepet/blob/main/docs/spec-v0.md','اسپک اولیهٔ V0'),('https://github.com/kasraabo/keepet/blob/main/docs/journey.fa.txt','نقشهٔ سفر اولیه')]:links[path]=label
items=list(links.items())
for start in range(0,len(items),11):
 parts.append('<section class="page directory" id="'+('directory' if start==0 else 'directory-'+str(start//11))+'"><div class="eyebrow">مرجع سریع</div><h2>فهرست لینک‌های قابل کلیک'+(' · ادامه' if start else '')+'</h2><p class="dir-intro">برای بخش خصوصی، با نقش مربوط وارد شوید. سندهای قدیمی ممکن است بعضی رفتارهای قبلی را توضیح دهند؛ برای رفتار فعلی متن همین دفترچه و راهنمای امکانات جدید مبناست.</p>')
 for path,label in items[start:start+11]:parts.append('<div class="route-box">'+route(label,path)+'</div>')
 if start==0:parts.append('<div class="note">مسیرهای /booking/{id}، /bookings/{id}، /chats/{id}، /magazine/{slug} و /pages/{slug} وابسته به رکورد واقعی‌اند. لینک هر کدام را از کارت مربوط کپی کنید؛ آدرس نمونهٔ رکورد محلی در این PDF منتشر نشده است.</div>')
 parts.append('<div class="back"><a href="#toc-0">بازگشت به فهرست ←</a></div></section>')
parts.append('</body></html>');OUT.write_text(''.join(parts),encoding='utf8');print(OUT, 'sections',len(PAGES),'unique links',len(links))
