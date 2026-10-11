const CACHE='keepet-static-v1';
const ASSETS=['/static/style.css','/static/app.js','/static/icon.svg','/static/pets.svg','/static/vendor/Vazirmatn-Regular.woff2','/static/vendor/Vazirmatn-Bold.woff2','/static/vendor/htmx.min.js','/static/vendor/alpine.min.js','/static/offline.html'];
self.addEventListener('install',e=>{e.waitUntil(caches.open(CACHE).then(c=>c.addAll(ASSETS)).then(()=>self.skipWaiting()))});
self.addEventListener('activate',e=>{e.waitUntil(caches.keys().then(keys=>Promise.all(keys.filter(k=>k!==CACHE).map(k=>caches.delete(k)))).then(()=>self.clients.claim()))});
self.addEventListener('fetch',e=>{const u=new URL(e.request.url);if(e.request.method!=='GET'||u.origin!==self.location.origin)return;if(u.pathname.startsWith('/static/')){e.respondWith(caches.match(e.request).then(cached=>cached||fetch(e.request)));return}if(e.request.mode==='navigate')e.respondWith(fetch(e.request).catch(()=>caches.match('/static/offline.html')))});
