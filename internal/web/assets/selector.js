'use strict';
(() => {
 const dialog=$('#visual-selector'), frameHost=$('#selector-frame-host'), state=$('#selector-status'), field=$('#selector-field'), choices=$('#selector-choices'), apply=$('#selector-apply'), matches=$('#selector-matches');
 const names={items:'Repeated items',title:'Title',link:'Link',content:'Description',image:'Image',date:'Date'}, fields=['title','link','content','image','date'];
 let frame=null, token='', snapshot=null, selected=null, pending=null, abort=null, revision=0, matchRevision=0, timer=null, ready=false, conversion=null, snapshotMode='static', snapshotFeedMode='static', modeChanged=false;
 function tell(data){frame?.contentWindow?.postMessage({...data,token},'*');} // Opaque destination; WindowProxy + nonce bind the recipient.
 function resetSelection(){selected=null;pending=null;choices.replaceChildren();apply.disabled=true;$('#selector-level-label').hidden=true;$('#selector-parent').disabled=true;$('#selector-selected').textContent='Click an element in the page preview.';}
 function matchStatus(text,error=false){matches.textContent=text;matches.classList.toggle('error',error);delete matches.dataset.count;delete matches.dataset.items;matches.dataset.error=String(error);}
 function cancelConversion(){if(conversion!==null)state.textContent='Edit a selector or click an element to continue.';conversion=null;$('#selector-convert').disabled=!ready;}
 function clear(){revision++;matchRevision++;clearTimeout(timer);abort?.abort();abort=null;frameHost.replaceChildren();frame=null;snapshot=null;token='';ready=false;cancelConversion();$('#selector-load').disabled=false;resetSelection();matchStatus('Load a page to highlight your selectors.');}
 function updateLabels(){
  window.dispatchEvent(new Event('rss-selector-syntax-change'));
  const xpath=form.elements.type.value==='xpath';
  $('#selector-choices-label').textContent=xpath?'Suggested XPath':'Suggested CSS';
  $('#selector-convert').hidden=xpath;
  $('#selector-syntax-help').textContent=xpath?'Use // for repeated items and relative fields such as .//h2 or .//a/@href.':'Type CSS directly, or convert your selectors to XPath to fine-tune them.';
  $('#visual-items').placeholder=xpath?'//article':'article.card';
  fields.forEach(k=>{$('#visual-'+k+'-selector').placeholder=xpath?{title:'.//h2',link:'.//a/@href',content:'.//p',image:'.//img',date:'.//time/@datetime'}[k]:{title:'h2',link:'a',content:'.summary',image:'img',date:'time'}[k];});
  for(const option of field.options)option.disabled=option.value!=='items'&&!form.elements.items.value.trim();
  if(field.selectedOptions[0]?.disabled)field.value='items';
  $('#selector-mapping').textContent=['items',...fields].map(k=>`${names[k]}: ${form.elements[k==='items'?'items':k+'_selector'].value||'—'}`).join('\n');
  for(const el of dialog.querySelectorAll('[data-highlight-field]'))el.classList.toggle('active-field',el.dataset.highlightField===field.value);
 }
 function fromForm(){
  $('#visual-type').value=form.elements.type.value;$('#visual-items').value=form.elements.items.value;
  for(const k of fields)for(const suffix of ['selector','attr'])$('#visual-'+k+'-'+suffix).value=form.elements[k+'_'+suffix].value;
  updateLabels();
 }
 function syncToForm(){
  form.elements.type.value=$('#visual-type').value;form.elements.items.value=$('#visual-items').value;
  for(const k of fields)for(const suffix of ['selector','attr'])form.elements[k+'_'+suffix].value=$('#visual-'+k+'-'+suffix).value;
  if(ready)form.elements.mode.value=snapshotFeedMode;
  help();renderHelp();$('#preview').replaceChildren();updateLabels();
 }
 function sendRecipe(){if(ready)tell({type:'recipe',revision:matchRevision,recipe:readForm().recipe,field:field.value});}
 function highlight(immediate=false){
  clearTimeout(timer);matchRevision++;cancelConversion();
  if(!ready)return;
  matchStatus('Updating highlights…');
  if(immediate)sendRecipe();else timer=setTimeout(sendRecipe,120);
 }
 function activeField(name){if(!(name in names))return;field.value=name;updateLabels();resetSelection();highlight(true);}
 function edited(name){syncToForm();if(name)field.value=name;resetSelection();updateLabels();highlight();}
 function pageModeHelp(){
  const mode=$('#selector-mode').value, tip=$('#selector-mode-help');
  tip.hidden=mode==='static';
  tip.textContent=mode==='flaresolverr'?(flaresolverrAvailable?'Uses the configured external browser. Some CAPTCHAs remain unsupported.':'FlareSolverr is not configured on this server. Set FLARESOLVERR_URL to load this page version.'):(browserAvailable?'Uses Chromium and the feed’s wait settings.':'Chromium is not configured on this server.');
 }
 async function loadSnapshot(){
  clear();const current=revision;abort=new AbortController();state.textContent='Loading page…';$('#selector-load').disabled=true;
  try{
   const f=readForm();snapshotMode=$('#selector-mode').value;snapshotFeedMode=f.recipe.mode==='auto'&&!modeChanged?'auto':snapshotMode;
   pageModeHelp();
   if(snapshotMode==='flaresolverr'&&!flaresolverrAvailable)throw new Error('FlareSolverr is not configured on this server. Choose another page version or configure FlareSolverr.');
   const data=await api('/selector/snapshot','POST',{url:f.url,recipe:{mode:snapshotMode,wait_selector:f.recipe.wait_selector,settle_ms:f.recipe.settle_ms}},abort.signal);
   if(current!==revision||!dialog.open)return;
   snapshot=data.nodes;$('#selector-source').textContent=data.url||f.url;token=Array.from(crypto.getRandomValues(new Uint8Array(16)),v=>v.toString(16).padStart(2,'0')).join('');
   fromForm();
   frame=document.createElement('iframe');frame.id='selector-frame';frame.title='Source page preview with live selector highlights';frame.setAttribute('sandbox','allow-scripts');frame.src='/selector/frame';frameHost.append(frame);
   state.textContent='Opening selection preview…';
  }catch(e){if(current===revision&&e.name!=='AbortError'){state.textContent=e.message;matchStatus('The page could not be loaded. You can still edit selectors.');}}
  finally{if(current===revision)$('#selector-load').disabled=false;}
 }
 $('#visual-button').onclick=()=>{
  if(!form.elements.url.reportValidity()||!form.elements.url.value)return;
  field.value='items';modeChanged=false;fromForm();$('#selector-source').textContent=form.elements.url.value;
  $('#selector-mode').value=['browser','flaresolverr'].includes(form.elements.mode.value)?form.elements.mode.value:'static';
  $('#selector-mode option[value="browser"]').disabled=!browserAvailable;
  $('#selector-mode option[value="flaresolverr"]').disabled=!flaresolverrAvailable;
  dialog.showModal();loadSnapshot();
 };
 $('#selector-mode').onchange=()=>{modeChanged=true;pageModeHelp();state.textContent='Reload the page to use this page version.';};
 $('#selector-load').onclick=loadSnapshot;$('#selector-close').onclick=()=>{if(ready){if(form.elements.mode.value!==snapshotFeedMode)$('#preview').replaceChildren();form.elements.mode.value=snapshotFeedMode;renderHelp();}dialog.close();};
 dialog.addEventListener('close',clear);
 window.addEventListener('rss-session-ended',()=>{if(dialog.open)dialog.close();});
 window.addEventListener('rss-theme-change',e=>tell({type:'theme',theme:e.detail}));
 $('#selector-parent').onclick=()=>tell({type:'parent'});
 $('#selector-level').onchange=()=>tell({type:'ancestor',node:Number($('#selector-level').value)});
 choices.onchange=()=>tell({type:'highlight',selector:choices.value});
 field.onchange=()=>activeField(field.value);
 $('#visual-type').onchange=()=>{edited();state.textContent='Selector type changed. Edit the expressions below to match this syntax.';};
 $('#visual-items').oninput=()=>edited('items');$('#visual-items').onfocus=()=>activeField('items');
 for(const k of fields)for(const suffix of ['selector','attr']){
  const input=$('#visual-'+k+'-'+suffix);input.oninput=()=>edited(k);input.onfocus=()=>activeField(k);
 }
 $('#selector-convert').onclick=()=>{
  if(!ready)return;clearTimeout(timer);matchRevision++;conversion=matchRevision;$('#selector-convert').disabled=true;
  state.textContent='Converting the current CSS selectors to XPath…';tell({type:'convert',revision:conversion,recipe:readForm().recipe});
 };
 apply.onclick=()=>{if(!selected)return;pending={field:field.value,selector:choices.value,attr:selected.attr,type:form.elements.type.value};apply.disabled=true;tell({type:'commit',selector:choices.value});};
 window.addEventListener('message',e=>{
  if(!dialog.open||!frame||e.source!==frame.contentWindow||e.origin!=='null'||!e.data||typeof e.data!=='object')return;
  const d=e.data;
  if(d.type==='ready'&&snapshot){tell({type:'init',nodes:snapshot,theme:document.documentElement.dataset.theme});snapshot=null;return;}
  if(!token||d.token!==token)return;
  if(d.type==='loaded'){ready=true;state.textContent='1. Click a headline or card, or edit a selector on the left. Focus a field to highlight it across the repeated items.';highlight(true);}
  if(d.type==='matches'&&d.revision===matchRevision&&d.field===field.value){
   if(typeof d.error!=='string'||d.error.length>1000||![d.items,d.count,d.visible].every(n=>Number.isInteger(n)&&n>=0&&n<=12000))return;
   const detail=field.value==='items'?`${d.items} repeated item${d.items===1?'':'s'}`:`${names[field.value]}: ${d.count} of ${d.items} item${d.items===1?'':'s'} matched`;
   matchStatus(d.error||detail+(d.visible<d.count?` · ${d.visible} visible in this simplified preview`:''),!!d.error);
   matches.dataset.count=String(d.count);matches.dataset.items=String(d.items);
  }
  if(d.type==='converted'&&conversion!==null&&d.revision===conversion){
   cancelConversion();
   if(d.error){state.textContent=String(d.error).slice(0,1000)+' Your selectors were kept.';sendRecipe();return;}
   const r=d.recipe;
   if(!r||r.type!=='xpath'||typeof r.items!=='string'||r.items.length>1000||!fields.every(k=>r[k]&&typeof r[k].selector==='string'&&r[k].selector.length<=1000&&typeof r[k].attr==='string'&&r[k].attr.length<=2048))return;
   form.elements.type.value='xpath';form.elements.items.value=r.items;
   for(const k of fields){form.elements[k+'_selector'].value=r[k].selector;form.elements[k+'_attr'].value=r[k].attr;}
   help();fromForm();resetSelection();$('#preview').replaceChildren();highlight(true);state.textContent='Converted to XPath. Fine-tune any expression and check its highlights on the right.';
  }
  if(d.type==='selection'&&d.field===field.value&&typeof d.label==='string'&&d.label.length<=200&&Array.isArray(d.choices)&&d.choices.length<=14){
   if(!d.choices.every(c=>c&&typeof c.selector==='string'&&c.selector.length<=1000&&Number.isInteger(c.count)&&c.count>=0&&c.count<=1000))return;
   selected={attr:['','href','datetime'].includes(d.attr)?d.attr:''};choices.replaceChildren();
   for(const c of d.choices){const o=node('option',`${c.selector} · ${c.count} ${field.value==='items'?'items':'cards with this field'}`);o.value=c.selector;choices.append(o);}
   const levels=$('#selector-level');levels.replaceChildren();
   if(Array.isArray(d.ancestors)&&d.ancestors.length<=12&&d.ancestors.every(a=>a&&Number.isInteger(a.node)&&a.node>=0&&a.node<12000&&typeof a.label==='string'&&a.label.length<=200)){
    for(const a of d.ancestors){const o=node('option',a.label);o.value=String(a.node);o.selected=a.selected===true;levels.append(o);}
   }
   $('#selector-level-label').hidden=field.value!=='items'||!levels.options.length;
   $('#selector-selected').textContent='Selected: '+d.label;$('#selector-parent').disabled=!d.hasParent;apply.disabled=!d.choices.length;
   state.textContent=d.choices.length?'Check the highlighted suggestion, then use it or keep editing your selectors.':field.value==='items'?'Choose a smaller element or enter a selector.':'Choose a field inside one of the matched items.';
  }
  if(d.type==='committed'&&pending&&d.field===pending.field&&d.selector===pending.selector&&(!d.selectorType||d.selectorType===pending.type)){
   const p=pending;pending=null;form.elements.mode.value=snapshotFeedMode;renderHelp();
   if(p.field==='items'){
    const changed=form.elements.items.value!==p.selector;form.elements.items.value=p.selector;
    if(changed)for(const k of fields){form.elements[k+'_selector'].value='';form.elements[k+'_attr'].value='';}
    field.value='title';resetSelection();
   }else{form.elements[p.field+'_selector'].value=p.selector;form.elements[p.field+'_attr'].value=p.attr;apply.disabled=false;}
   fromForm();$('#preview').replaceChildren();highlight(true);
   state.textContent=p.field==='items'?'2. Click a title in any matched card, or type its selector. Add other fields as needed.':names[p.field]+' assigned. Fine-tune its selector or choose another field.';
  }
 });
})();
