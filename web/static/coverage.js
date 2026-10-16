(()=>{
 if(window.KeepetCoverage){window.KeepetCoverage.enhance();return}
 let leaflet;
 function loadLeaflet(){
  if(window.L)return Promise.resolve(window.L);
  if(!leaflet)leaflet=new Promise((resolve,reject)=>{
   const css=document.createElement('link');css.rel='stylesheet';css.href='/static/vendor/leaflet/leaflet.css';document.head.append(css);
   const script=document.createElement('script');script.src='/static/vendor/leaflet/leaflet.js';script.onload=()=>resolve(window.L);script.onerror=()=>reject(Error('map library unavailable'));document.head.append(script);
  });return leaflet;
 }
 let geography;
 function data(){return geography||(geography=fetch('/static/maps/tehran-districts.geojson').then(r=>{if(!r.ok)throw Error('boundary data unavailable');return r.json()}))}
 function setup(root){
  if(root.dataset.started)return;root.dataset.started='true';
  const shapes=[...root.querySelectorAll('[data-district]')],state=new Map(shapes.map(p=>[p.dataset.district,{name:p.dataset.name,enabled:p.dataset.enabled==='true'}]));
  const manage=root.dataset.management==='true',select=root.closest('.area-picker')?.querySelector('[data-coverage-select]'),status=root.querySelector('.coverage-map-status');let map,layer;
  function chosen(){return select?.value||''}
  function paint(){
   for(const p of shapes){const selected=chosen()===p.dataset.district;p.classList.toggle('selected',selected);p.setAttribute('aria-pressed',String(selected))}
   if(layer)layer.setStyle(style);
  }
  function style(f){const info=state.get(f.properties.id),selected=chosen()===f.properties.id;return {color:selected?'#176ec3':'#fff',weight:selected?3:1.5,fillColor:selected?'#438ed7':info?.enabled?'#258462':'#939da4',fillOpacity:info?.enabled ? 0.48 : 0.28}}
  function pick(id){
   const info=state.get(id);if(!info)return;
   if(manage){const field=document.getElementById('coverage-'+id);field?.scrollIntoView({behavior:'smooth',block:'center'});field?.querySelector('[name=enabled]')?.focus({preventScroll:true});status.textContent=info.name+' · '+(info.enabled?'تحت پوشش':'بدون پوشش');return}
   if(!info.enabled){status.textContent=info.name+' فعلاً تحت پوشش نیست. منطقهٔ دیگری انتخاب کنید.';return}
   if(!select||![...select.options].some(o=>o.value===id))return;
   select.value=id;select.dispatchEvent(new Event('input',{bubbles:true}));select.dispatchEvent(new Event('change',{bubbles:true}));paint();status.textContent=info.name+' انتخاب شد.';
  }
  root.addEventListener('click',e=>{const p=e.target.closest('[data-district]');if(p)pick(p.dataset.district)});
  root.addEventListener('keydown',e=>{const p=e.target.closest('[data-district]');if(p&&['Enter',' '].includes(e.key)){e.preventDefault();pick(p.dataset.district)}});
  select?.addEventListener('change',()=>{paint();const current=state.get(chosen());status.textContent=current?current.name+' انتخاب شد.':chosen()&&chosen()!=='outside'?'محدودهٔ محله‌ای قبلی از فهرست انتخاب شده است.':'برای انتخاب، روی منطقهٔ تحت پوشش بزنید.'});paint();
  Promise.all([loadLeaflet(),data()]).then(([L,geo])=>{
   if(!root.isConnected)return;
   const host=root.querySelector('.coverage-leaflet');host.hidden=false;
   map=L.map(host,{scrollWheelZoom:false,keyboard:true,zoomSnap:0.25,minZoom:9,maxZoom:18,tapTolerance:20});root._coverageMap=map;
   const note=document.createElement('p');note.className='coverage-basemap-note small-text muted';root.querySelector('.coverage-attribution').before(note);
   const tiles=L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png',{maxZoom:19,attribution:'&copy; <a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener noreferrer">OpenStreetMap</a>'}).addTo(map);
   tiles.on('tileerror',()=>{note.textContent='پس‌زمینهٔ خیابانی در دسترس نیست؛ مرزهای واقعی مناطق همچنان قابل انتخاب‌اند.'});
   layer=L.geoJSON(geo,{style,onEachFeature:(f,item)=>{
    const id=f.properties.id,info=state.get(id);if(!info)return;
    const title=document.createElement('span');title.textContent=info.name+' · '+(info.enabled?'تحت پوشش':'فعلاً بدون پوشش');item.bindTooltip(title,{sticky:true});item.on('click',()=>pick(id));
    // Use the same interior coordinate as the server fallback, projected by Leaflet.
    const fallback=shapes.find(p=>p.dataset.district===id),text=fallback?.nextElementSibling;
    if(text){
     // The interior WGS84 label is supplied with the geographic feature.
     if(f.properties.label){const button=document.createElement('button');button.type='button';button.className='district-map-label';button.textContent=new Intl.NumberFormat('fa-IR').format(f.properties.number);button.setAttribute('aria-label',title.textContent);button.setAttribute('aria-disabled',String(!manage&&!info.enabled));button.addEventListener('click',e=>{e.stopPropagation();pick(id)});L.marker(f.properties.label,{icon:L.divIcon({className:'district-label-marker',html:button,iconSize:[22,22],iconAnchor:[11,11]})}).addTo(map)}
    }
   }}).addTo(map);
   map.fitBounds(layer.getBounds(),{padding:[12,12]});map.setMaxBounds(layer.getBounds().pad(.6));root.querySelector('.coverage-fallback').hidden=true;
   requestAnimationFrame(()=>map.invalidateSize());
  }).catch(()=>{const host=root.querySelector('.coverage-leaflet');if(map){map.remove();root._coverageMap=null}host.hidden=true;root.querySelector('.coverage-fallback').hidden=false});
 }
 function enhance(){document.querySelectorAll('[data-coverage-map]').forEach(setup)}
 window.KeepetCoverage={enhance};document.addEventListener('DOMContentLoaded',enhance);document.addEventListener('htmx:afterSwap',enhance);
 document.addEventListener('htmx:beforeCleanupElement',e=>{e.detail.elt.querySelectorAll?.('[data-coverage-map]').forEach(el=>{el._coverageMap?.remove();el._coverageMap=null})});
 enhance();
})();
