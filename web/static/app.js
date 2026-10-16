function enhance(root=document){
 const date=new Intl.DateTimeFormat('fa-IR-u-ca-persian',{day:'numeric',month:'long',year:'numeric',weekday:'short',timeZone:'Asia/Tehran'});
 const datetime=new Intl.DateTimeFormat('fa-IR-u-ca-persian',{day:'numeric',month:'long',year:'numeric',hour:'2-digit',minute:'2-digit',timeZone:'Asia/Tehran'});
 root.querySelectorAll('[data-date]').forEach(el=>{const d=new Date(el.dataset.date+'T12:00:00+03:30');if(!isNaN(d))el.textContent=date.format(d)});
 root.querySelectorAll('[data-datetime]').forEach(el=>{const d=new Date(el.dataset.datetime);if(!isNaN(d))el.textContent=datetime.format(d)});
 root.querySelectorAll('[data-countdown]').forEach(el=>{if(el.dataset.started)return;el.dataset.started='true';const end=new Date(el.dataset.countdown).getTime()+30*60000;const update=()=>{if(!el.isConnected){clearInterval(timer);return}const left=Math.max(0,Math.ceil((end-Date.now())/1000));const display=el.querySelector('[data-timer]');if(display)display.textContent=[Math.floor(left/60),left%60].map(n=>String(n).padStart(2,'0')).join(':').replace(/\d/g,d=>'۰۱۲۳۴۵۶۷۸۹'[d]);if(left===0){const label=el.querySelector('[data-timer-label]');if(label)label.textContent='زمان هدف گذشته؛ پشتیبانی همراه شماست.';clearInterval(timer)}};const timer=setInterval(update,1000);update()});
}
document.addEventListener('DOMContentLoaded',()=>enhance());
document.addEventListener('htmx:afterSwap',e=>{enhance();if(!['booking-pet-fields','provider-results'].includes(e.detail.target?.id))window.scrollTo({top:0,behavior:'instant'})});
if('serviceWorker' in navigator)window.addEventListener('load',()=>navigator.serviceWorker.register('/sw.js').catch(()=>{}));
let installPrompt;window.addEventListener('beforeinstallprompt',e=>{e.preventDefault();installPrompt=e;document.querySelectorAll('[data-install]').forEach(b=>b.hidden=false)});
document.addEventListener('click',async e=>{if(e.target.closest('[data-install]')&&installPrompt){await installPrompt.prompt();installPrompt=null;document.querySelectorAll('[data-install]').forEach(b=>b.hidden=true)}});
