'use strict';
const $ = s => document.querySelector(s);
let browserAvailable=false, flaresolverrAvailable=false;
let csrf = '', feeds = [], polling;
const form = $('#recipe-form');
function notice(text, success=false) { const n=$('#notice'); n.textContent=text; n.classList.toggle('success',success); n.hidden=!text; }
async function api(path, method='GET', data, signal) {
 const res=await fetch('/api'+path,{method,signal,headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},body:data===undefined?undefined:JSON.stringify(data)});
 const raw=await res.text(); let out;try{out=JSON.parse(raw);}catch{out={error:raw.trim()};}
 if(signal?.aborted)throw new DOMException('Request cancelled','AbortError');
 if(!res.ok){
  if(res.status===401&&path!='/login')showLogin();
  const error=new Error(typeof out?.error==='string'&&out.error?out.error:'Request failed');
  error.status=res.status;
  error.matches=Number.isSafeInteger(out?.matches)&&out.matches>=0?out.matches:null;
  error.valid=Number.isSafeInteger(out?.valid)&&out.valid>=0?out.valid:0;
  error.filtered=Number.isSafeInteger(out?.filtered)&&out.filtered>=0?out.filtered:0;
  error.filter_examples=Array.isArray(out?.filter_examples)?out.filter_examples:[];
  error.warnings=Array.isArray(out?.warnings)?out.warnings.filter(warning=>typeof warning==='string'):[];
  error.diagnostics=window.rssDiagnostics?.normalize(out?.diagnostics)||null;
  throw error;
 }return out;
}
function node(tag,text,cls){const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;}
function action(label,fn,cls='quiet'){const b=node('button',label,cls);b.type='button';b.onclick=()=>busy(b,fn);return b;}
async function busy(button,fn){button.disabled=true;notice('');try{await fn();}catch(e){notice(e.message);}finally{button.disabled=false;}}
function when(v){return !v||v.startsWith('0001-')?'—':new Date(v).toLocaleString();}
function previewDate(item){
 const date=when(item.published), p=node('p',undefined,'meta');
 if(item.published_estimated&&date!=='—'){
  p.classList.add('estimated-date');p.append(node('span','Estimated date','estimate-label'),node('span',date));
  if(item.published_source)p.append(node('span',`from “${item.published_source}”`,'estimate-source'));
 }else p.textContent=date==='—'?'Date: first seen when saved':date;
 return p;
}
function showLogin(){clearInterval(polling);$('#workspace').hidden=true;$('#logout').hidden=true;$('#login-panel').hidden=false;csrf='';window.dispatchEvent(new Event('rss-session-ended'));}
async function showApp(){ $('#login-panel').hidden=true;$('#workspace').hidden=false;$('#logout').hidden=false;await load();clearInterval(polling);polling=setInterval(()=>{if(!document.hidden)load().catch(e=>notice(e.message));},10000); }
async function load(){
 const out=await api('/feeds');feeds=out.feeds;browserAvailable=out.browser?.capacity>0;flaresolverrAvailable=out.flaresolverr?.capacity>0;
 $('#queue').textContent=`${feeds.length} feed${feeds.length===1?'':'s'} · ${out.active} of ${out.capacity} refresh slots in use · ${out.due} due · Chromium ${browserAvailable?`${out.browser.active}/${out.browser.capacity} slots`:"not configured"} · FlareSolverr ${flaresolverrAvailable?`${out.flaresolverr.active}/${out.flaresolverr.capacity} slots`:"not configured"}`;
 if(!$('#editor').hidden)renderHelp();
 $('#empty').hidden=feeds.length>0;$('#export-recipes').hidden=feeds.length===0;const list=$('#feeds');list.replaceChildren();
 for(const f of feeds){
  const card=node('article',undefined,'feed-card'+(f.enabled?'':' paused'));const top=node('div',undefined,'feed-top');top.append(node('h2',f.title),node('span',!f.enabled?'Paused':f.error?'Needs attention':f.last_success.startsWith('0001-')?'Pending':'Active','badge'+(f.error?' error':'')));card.append(top);
  const source=node('a',f.url,'source');source.href=f.url;source.target='_blank';source.rel='noopener noreferrer';card.append(source);
  const times=node('dl',undefined,'times');for(const [label,value] of [['Saved items',f.count],['Last attempt',when(f.last_attempt)],['Last success',when(f.last_success)],['Next refresh',f.enabled?when(f.next_run):'Paused']]){const pair=node('div');pair.append(node('dt',label),node('dd',String(value)));times.append(pair);}card.append(times);
  if(f.error)card.append(node('p',f.error,'diagnostic'));
  const actions=node('div',undefined,'feed-actions');actions.append(action('Edit',()=>openEditor(f)),action('Refresh',async()=>{await api(`/feeds/${f.id}/refresh`,'POST',{});notice('Refresh queued.',true);await load();}),action(f.enabled?'Pause':'Resume',async()=>{await api(`/feeds/${f.id}`,'PUT',payload(f,!f.enabled));await load();}),action('Delete',async()=>{if(!confirm(`Delete “${f.title}” and all its saved items? This cannot be undone.`))return;await api(`/feeds/${f.id}`,'DELETE',{});if(form.elements.id.value===f.id)$('#editor').hidden=true;await load();},'quiet danger'));card.append(actions);
  const diagnostics=node('button','Diagnostics','quiet feed-diagnostics');diagnostics.type='button';diagnostics.onclick=()=>window.rssDiagnostics.open(f);diagnostics.dataset.feedId=f.id;actions.append(diagnostics);
  const exportLink=node('a','Export recipe','quiet button-link');exportLink.href='/api/recipes/export?id='+encodeURIComponent(f.id);exportLink.download='';actions.append(exportLink);
  const links=node('div',undefined,'feed-links');
  for(const [format,url] of [['RSS',f.rss_url],['Atom',f.atom_url]]){
   if(!url)continue;
   const row=node('div',undefined,'feed-format'),link=node('a',`Open ${format} feed ↗`,'rss-link');link.href=url;link.target='_blank';link.rel='noopener noreferrer';row.append(link,action(`Copy ${format} URL`,()=>copyFeedURL(format,url)));links.append(row);
  }
  links.append(action('Reset feed links',async()=>{if(!confirm('Reset both RSS and Atom links? Readers using either old link will need the new one.'))return;await api(`/feeds/${f.id}/rotate-token`,'POST',{});await load();}));card.append(links);list.append(card);
 }
 window.dispatchEvent(new CustomEvent('rss-feeds-loaded',{detail:feeds.map(f=>f.id)}));
}
async function copyFeedURL(format,url){
 if(navigator.clipboard&&window.isSecureContext){try{await navigator.clipboard.writeText(url);notice(format+' URL copied.',true);return;}catch{}}
 window.prompt('Copy this '+format+' URL:',url);
}
function payload(f,enabled=f.enabled){return {title:f.title,url:f.url,recipe:f.recipe,interval:f.interval,enabled};}
function openEditor(f){
 form.reset();$('#preview').replaceChildren();$('#editor-title').textContent=f?'Edit feed':'New feed';form.elements.id.value=f?.id||'';
 if(f){form.elements.mode.value=f.recipe.mode||"static";form.elements.wait_selector.value=f.recipe.wait_selector||"";form.elements.settle_ms.value=f.recipe.settle_ms||0;form.elements.title.value=f.title;form.elements.url.value=f.url;form.elements.type.value=f.recipe.type;form.elements.items.value=f.recipe.items;form.elements.interval.value=f.interval/60;form.elements.enabled.checked=f.enabled;for(const k of ['title','link','content','image','date']){form.elements[k+'_selector'].value=f.recipe[k].selector;form.elements[k+'_attr'].value=f.recipe[k].attr;}form.elements.date_layout.value=f.recipe.date_layout;form.elements.timezone.value=f.recipe.timezone;
  const full=f.recipe.full_content;form.elements.full_selector.value=full?.selector||'';form.elements.full_attr.value=full?.attr||'';form.elements.full_browser.checked=!!full?.browser;}
 window.rssFilters?.load(f?.recipe.filters,!!f);$('#editor').hidden=false;renderHelp();help();fullContentSummary();form.elements.title.focus();
}
function readForm(){const v=new FormData(form);const recipe={mode:v.get('mode'),wait_selector:v.get('wait_selector'),settle_ms:Number(v.get('settle_ms')),type:v.get('type'),items:v.get('items'),date_layout:v.get('date_layout'),timezone:v.get('timezone')};for(const k of ['title','link','content','image','date'])recipe[k]={selector:v.get(k+'_selector'),attr:v.get(k+'_attr')};const filters=window.rssFilters?.read();if(filters)recipe.filters=filters;
 const fullSelector=(v.get('full_selector')||'').trim();
 if(fullSelector)recipe.full_content={selector:fullSelector,attr:v.get('full_attr')||'',browser:form.elements.full_browser.checked};
 return {title:v.get('title'),url:v.get('url'),interval:Number(v.get('interval'))*60,enabled:form.elements.enabled.checked,recipe};}
function fullContentSummary(){
 const on=(form.elements.full_selector?.value||'').trim();
 const label=$('#full-summary');if(label)label.textContent=on?'On':'Off';
}
function renderHelp(){
 const mode=form.elements.mode.value, localBrowser=mode==='browser'||mode==='auto', tip=$('#fetch-mode-help');
 $('#render-settings').hidden=!localBrowser;
 tip.classList.toggle('diagnostic',localBrowser&&!browserAvailable||mode==='flaresolverr'&&!flaresolverrAvailable);
 if(mode==='flaresolverr')tip.textContent=flaresolverrAvailable?'Uses the configured external browser for Cloudflare challenge pages. Some CAPTCHAs remain unsupported.':'FlareSolverr is not configured on this server. Set FLARESOLVERR_URL before previewing or refreshing in this mode.';
 else if(localBrowser&&!browserAvailable)tip.textContent='Chromium is not configured on this server. Enable the browser deployment before using this mode.';
 else if(mode==='auto')tip.textContent='Tries static HTML first, then Chromium if no items are found. Access blocks do not trigger a retry.';
 else tip.textContent=mode==='browser'?'Renders the page with the server’s Chromium browser before extracting items.':'Fetches the page’s HTML without running JavaScript.';
}
form.elements.mode.onchange=renderHelp;
function help(){$('#selector-help').textContent=form.elements.type.value==='xpath'?'Use relative XPath fields starting with ., such as .//h2 or .//a/@href. Use . for the card itself.':'Fields are relative to each card. Use . for the card itself. Choose an attribute separately, such as href or datetime.';}
$('#login-form').onsubmit=e=>{e.preventDefault();busy(e.submitter,async()=>{const out=await api('/login','POST',{password:$('#password').value});csrf=out.csrf;$('#password').value='';await showApp();});};
$('#logout').onclick=()=>busy($('#logout'),async()=>{await api('/logout','POST',{});showLogin();});
$('#new-feed').onclick=$('#empty-new').onclick=()=>openEditor();$('#close-editor').onclick=()=>$('#editor').hidden=true;form.elements.type.onchange=help;
form.elements.full_selector.oninput=fullContentSummary;
form.onsubmit=e=>{e.preventDefault();busy(e.submitter,async()=>{const id=form.elements.id.value,data=readForm();if(id)data.apply_filters_to_history=$('#apply-filters-to-history').checked;await api(id?`/feeds/${id}`:'/feeds',id?'PUT':'POST',data);$('#editor').hidden=true;notice('Feed saved. Enabled feeds refresh in the background.',true);await load();});};
$('#preview-button').onclick=()=>{if(!form.reportValidity())return;busy($('#preview-button'),async()=>{
 const p=$('#preview');p.replaceChildren(node('p','Fetching and extracting…','hint'));
 try{
  const out=await api('/preview','POST',readForm());p.replaceChildren(node('h2',`${out.items.length} items · ${out.matches} matches`));
  window.rssFilters?.preview(p,out);
  for(const warning of out.warnings)p.append(node('p',warning,'diagnostic'));
  if(out.items.some(item=>item.published_estimated)){
   const note=node('p','Preview estimates use this fetch time. Saved stories keep the publication date recorded on first discovery.','hint');note.id='preview-date-note';p.append(note);
  }
  for(const item of out.items){
   const card=node('article',undefined,'preview-item'),title=node(item.url?'a':'h3',item.title);
   if(item.url){title.href=item.url;title.target='_blank';title.rel='noopener noreferrer';}card.append(title,previewDate(item));
   const content=node('div');content.innerHTML=item.html;for(const a of content.querySelectorAll('a')){a.target='_blank';a.rel='noopener noreferrer';}card.append(content);p.append(card);
  }
  window.rssDiagnostics?.appendPreview(p,out.diagnostics);
 }catch(e){
  p.replaceChildren(node('p','Preview could not be completed. Check the message above.','hint'));
  if(e.status===422&&e.matches!==null){
   p.prepend(node('h2',`0 items · ${e.matches} matches`));
   window.rssFilters?.preview(p,{items:[],valid:e.valid,filtered:e.filtered,filter_examples:e.filter_examples},true);
   for(const warning of e.warnings)p.append(node('p',warning,'diagnostic'));
  }
  window.rssDiagnostics?.appendPreview(p,e.diagnostics);
  throw e;
 }
});};
(async()=>{try{const s=await api('/session');csrf=s.csrf;await showApp();}catch(e){showLogin();}})();
