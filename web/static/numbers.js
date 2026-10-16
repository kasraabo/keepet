/* Persian numeric UI. Identifiers, bank digits, URLs and secrets retain their
   original values; KeepetDigits supplies Persian glyphs without corrupting them. */
(()=>{
 const ascii=v=>String(v).replace(/[۰-۹]/g,c=>String(c.charCodeAt(0)-1776)).replace(/[٠-٩]/g,c=>String(c.charCodeAt(0)-1632)).replace(/٫/g,'.');
 const raw=v=>ascii(v).replace(/[٬,]/g,'');
 const fa=v=>String(v).replace(/\d/g,c=>'۰۱۲۳۴۵۶۷۸۹'[c]);
 const moneyNames=new Set(['amount','wallet_amount','price','min','max','daily']);
 function format(v){v=raw(v);return /^[+-]?\d+$/.test(v)?fa(v.replace(/\B(?=(\d{3})+(?!\d))/g,'٬')):fa(v)}
 function validate(el){
  if(el.disabled)return;
  const v=raw(el.value);let error='';
  if(v){
   if(el.dataset.numeric==='time'){if(!/^([01]\d|2[0-3]):[0-5]\d$/.test(v))error='ساعت را به صورت ۰۹:۳۰ وارد کنید.'}
   else if(!/^[+-]?\d+(?:\.\d+)?$/.test(v)||(el.dataset.money!==undefined&&!/^\d+$/.test(v)))error=el.dataset.money!==undefined?'مبلغ را با عدد صحیح وارد کنید.':'عدد معتبر وارد کنید.';
   else if(el.hasAttribute('min')&&Number(v)<Number(el.min))error='حداقل مجاز '+format(el.min)+' است.';
   else if(el.hasAttribute('max')&&Number(v)>Number(el.max))error='حداکثر مجاز '+format(el.max)+' است.';
  }
  el.setCustomValidity(error);
 }
 function bind(el){
  if(el.dataset.numericBound)return;el.dataset.numericBound='1';
  const type=el.type;const isMoney=el.hasAttribute('data-money')||moneyNames.has(el.name);
  el.dataset.numeric=type==='time'?'time':'number';if(isMoney)el.dataset.money='';
  el.type='text';el.inputMode='numeric';el.dir='ltr';
  if(type==='time'){el.placeholder='۰۹:۳۰';el.maxLength=5}else if(isMoney)el.value=format(el.value);
  const update=()=>{
   if(isMoney){const pos=el.selectionStart??el.value.length;const before=raw(el.value.slice(0,pos)).length;el.value=format(el.value);let cursor=0,seen=0;while(cursor<el.value.length&&seen<before){if(el.value[cursor]!=='٬')seen++;cursor++}if(document.activeElement===el)el.setSelectionRange(cursor,cursor)}
   validate(el);
  };
  el.addEventListener('paste',e=>{if(!isMoney)return;const pasted=ascii(e.clipboardData?.getData('text')||'').trim();if(/[٬,]/.test(pasted)&&!/^\d{1,3}(?:[٬,]\d{3})+$/.test(pasted)){e.preventDefault();el.setCustomValidity('جداکنندهٔ مبلغ باید سه‌رقمی باشد؛ مانند ۱٬۰۰۰.');el.reportValidity()}});
  el.addEventListener('input',update);el.addEventListener('change',update);
  // FormData is used by both native form submission and HTMX; never submit separators.
  if(el.form&&!el.form.dataset.numericForm){el.form.dataset.numericForm='1';el.form.addEventListener('formdata',e=>el.form.querySelectorAll('[data-numeric]').forEach(i=>{if(!i.disabled&&i.name)e.formData.set(i.name,raw(i.value))}));}
 }
 function init(root=document){root.querySelectorAll('input[type=number],input[type=time],input[data-money]').forEach(bind)}
 document.addEventListener('input',e=>{if(e.target.matches('input[data-numeric="time"]'))e.target.value=ascii(e.target.value)},true);
 document.addEventListener('submit',e=>{if(e.submitter?.formNoValidate||e.target.noValidate)return;e.target.querySelectorAll('[data-numeric]').forEach(validate);if(!e.target.checkValidity()){e.preventDefault();e.target.reportValidity()}},true);
 document.addEventListener('htmx:configRequest',e=>e.target.querySelectorAll?.('[data-numeric]').forEach(i=>{if(!i.disabled&&i.name)e.detail.parameters[i.name]=raw(i.value)}));
 document.addEventListener('DOMContentLoaded',()=>init());document.addEventListener('htmx:afterSwap',()=>init());
 const watch=new MutationObserver(records=>{for(const r of records)for(const n of r.addedNodes)if(n.nodeType===1){if(n.matches('input[type=number],input[type=time],input[data-money]'))bind(n);init(n)}});
 watch.observe(document.documentElement,{childList:true,subtree:true});
 window.KeepetNumbers={ascii,raw,format,fa};if(document.readyState!=='loading')init();
})();
