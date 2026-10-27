const {chromium}=require('playwright');
const base=process.env.BASE_URL||'http://127.0.0.1:8115';
if(!['127.0.0.1','localhost'].includes(new URL(base).hostname))throw Error('local test server with fake Google configuration required');
(async()=>{const browser=await chromium.launch({executablePath:'/usr/bin/chromium',args:['--no-sandbox']});try{
for(const javaScriptEnabled of [true,false]){
 const c=await browser.newContext({javaScriptEnabled,viewport:{width:javaScriptEnabled?390:1440,height:1000},serviceWorkers:'block'}),p=await c.newPage();let authorization;
 await p.route('https://accounts.google.com/**',async route=>{authorization=new URL(route.request().url());await route.fulfill({contentType:'text/html',body:'<!doctype html><h1>Google navigation intercepted locally</h1>'})});
 await p.goto(base+'/provider/login');await p.getByRole('button',{name:'ادامه با Google / Gmail',exact:true}).waitFor();
 if(javaScriptEnabled)await p.screenshot({path:'/tmp/keepet-google-login-mobile.png',fullPage:true});
 await p.getByRole('button',{name:'ادامه با Google / Gmail',exact:true}).click();
 if(!javaScriptEnabled){await p.getByRole('heading',{name:'ادامهٔ ورود با گوگل',exact:true}).waitFor();await p.getByRole('link',{name:'ادامه در Google',exact:true}).click()}
 await p.getByRole('heading',{name:'Google navigation intercepted locally'}).waitFor();
 if(authorization.searchParams.get('code_challenge_method')!=='S256'||!authorization.searchParams.get('state')||!authorization.searchParams.get('nonce')||authorization.searchParams.get('client_id')!=='browser-test-client'||authorization.searchParams.has('client_secret'))throw Error('invalid Google navigation');await c.close();
}
console.log('PASS: Google navigation under the real CSP, with JavaScript and without JavaScript; state, nonce and PKCE; no client secret in authorization URL; all Google traffic intercepted locally.');
}finally{await browser.close()}})().catch(e=>{console.error(e);process.exit(1)});
