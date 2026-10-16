(()=>{if(window.KeepetReferrals)return;window.KeepetReferrals=true;
function enhance(){document.querySelectorAll('[data-invite-path]').forEach(input=>{input.value=new URL(input.dataset.invitePath,location.origin).href})}
document.addEventListener('DOMContentLoaded',enhance);document.addEventListener('htmx:afterSwap',enhance);enhance();
document.addEventListener('click',async e=>{const button=e.target.closest('[data-copy-referral]');if(!button)return;const input=document.getElementById(button.dataset.copyReferral),status=document.querySelector('[data-referral-copy-status]');if(!input)return;let ok=false;
try{if(navigator.clipboard&&window.isSecureContext){await navigator.clipboard.writeText(input.value);ok=true}else{input.focus();input.select();ok=document.execCommand('copy')}}catch{}
if(status)status.textContent=ok?'کپی شد؛ می‌توانید برای دوستتان بفرستید.':'متن را انتخاب کرده و از گزینهٔ کپی مرورگر استفاده کنید.';
});})();
