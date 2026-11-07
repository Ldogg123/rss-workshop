'use strict';
const $ = s => document.querySelector(s);
let browserAvailable=false, flaresolverrAvailable=false;
let csrf = '', feeds = [], polling;
// The enabled value the open editor loaded. Saving sends it back, so a feed
// paused or resumed from its card meanwhile keeps that state unless the
// operator changed the checkbox.
let loadedEnabled = null;
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
async function showApp(){ $('#login-panel').hidden=true;$('#workspace').hidden=false;$('#logout').hidden=false;await window.rssLibrary?.refresh().catch(e=>notice(e.message));await load();clearInterval(polling);polling=setInterval(()=>{if(!document.hidden)load().catch(e=>notice(e.message));},10000); }
async function load(){
 const out=await api('/feeds');feeds=out.feeds;browserAvailable=out.browser?.capacity>0;flaresolverrAvailable=out.flaresolverr?.capacity>0;
 $('#queue').textContent=`${feeds.length} feed${feeds.length===1?'':'s'} · ${out.active} of ${out.capacity} refresh slots in use · ${out.due} due · Chromium ${browserAvailable?`${out.browser.active}/${out.browser.capacity} slots`:"not configured"} · FlareSolverr ${flaresolverrAvailable?`${out.flaresolverr.active}/${out.flaresolverr.capacity} slots`:"not configured"}`;
 renderNotifications(out.notifications);
 if(!$('#editor').hidden)renderHelp();
 $('#empty').hidden=feeds.length>0;$('#export-recipes').hidden=feeds.length===0;renderLibrary();
 window.dispatchEvent(new CustomEvent('rss-feeds-loaded',{detail:feeds.map(f=>f.id)}));
}
// Notifications are set up in the server's environment, never here; the
// dashboard only says which channel is on and offers a test message.
const notificationLabels={json:'JSON webhook',ntfy:'ntfy'};
function renderNotifications(kind){
 const on=Object.hasOwn(notificationLabels,kind);
 $('#notifications').textContent=`Notifications: ${on?notificationLabels[kind]:'off'}`;
 $('#test-notification').hidden=!on;
}
$('#test-notification').onclick=()=>busy($('#test-notification'),async()=>{await api('/notifications/test','POST',{});notice('Test notification sent. Check that it arrived where NOTIFY_URL delivers.',true);});
// The library toolbar filters and sorts the feeds the last load fetched, so
// changing it requests nothing, and the poll redraws with what is selected.
// Status and sort are remembered in this browser; the search is not, so a
// returning visitor never finds the library narrowed by a forgotten word.
const view={search:'',status:'all',sort:'title'},viewKey='rss-library-view';
const statusLabels={active:'Active',quiet:'Quiet',failing:'Needs attention',held:'Waiting to retry',pending:'Pending',paused:'Paused'};
const stamp=v=>!v||v.startsWith('0001-')?null:Date.parse(v);
// A feed has one status, in this order, and its badge and the status filter
// both use it. The server reports quiet only for a feed that is none of the
// others.
function feedStatus(f){return !f.enabled?'paused':f.held?'held':f.error?'failing':stamp(f.last_success)===null?'pending':f.quiet?'quiet':'active';}
const collator=new Intl.Collator(undefined,{numeric:true,sensitivity:'base'});
const byTitle=(a,b)=>collator.compare(a.title,b.title)||(a.id<b.id?-1:a.id>b.id?1:0);
// Missing times sort as given: a feed with no next refresh last, one that
// never succeeded or never published first, as the oldest.
const byTime=(a,b,missing)=>{const x=a??missing,y=b??missing;return x===y?0:x<y?-1:1;};
const sorters={
 title:()=>0,
 next:(a,b)=>byTime(a.enabled&&!a.held?stamp(a.next_run):null,b.enabled&&!b.held?stamp(b.next_run):null,Infinity),
 success:(a,b)=>byTime(stamp(a.last_success),stamp(b.last_success),-Infinity),
 changed:(a,b)=>byTime(stamp(a.last_changed),stamp(b.last_changed),-Infinity),
 failures:(a,b)=>(b.failures||0)-(a.failures||0),
};
const fold=v=>String(v||'').normalize('NFKC').toLowerCase().replace(/\s+/g,' ').trim();
// The site as the card shows it, without a path or credentials.
const sourceHost=url=>/^[a-z][a-z0-9+.-]*:\/\/(?:[^@/?#]*@)?([^/?#]*)/i.exec(url||'')?.[1]||'';
function restoreView(){
 try{const saved=JSON.parse(localStorage.getItem(viewKey)||'null');if(Object.hasOwn(statusLabels,saved?.status))view.status=saved.status;if(Object.hasOwn(sorters,saved?.sort))view.sort=saved.sort;}catch{}
 $('#feed-status').value=view.status;$('#feed-sort').value=view.sort;
}
function saveView(){try{localStorage.setItem(viewKey,JSON.stringify({status:view.status,sort:view.sort}));}catch{}}
function renderLibrary(){
 const list=$('#feeds'),q=fold(view.search);
 const shown=feeds.filter(f=>(view.status==='all'||feedStatus(f)===view.status)&&(!q||fold(f.title).includes(q)||fold(sourceHost(f.url)).includes(q))).sort((a,b)=>sorters[view.sort](a,b)||byTitle(a,b));
 // Every card is rebuilt, so put keyboard focus back on the same control of
 // the same feed; the toolbar sits outside the list and keeps its own.
 const focused=document.activeElement,from=focused?.closest?.('.feed-card');
 const keep=from&&list.contains(from)&&focused.dataset.focus?{id:from.dataset.feedId,key:focused.dataset.focus}:null;
 list.replaceChildren(...shown.map(feedCard));
 if(keep)list.querySelector(`.feed-card[data-feed-id="${CSS.escape(keep.id)}"] [data-focus="${CSS.escape(keep.key)}"]`)?.focus({preventScroll:true});
 $('#library-tools').hidden=feeds.length===0;$('#no-matches').hidden=feeds.length===0||shown.length>0;
 const total=`${feeds.length} feed${feeds.length===1?'':'s'}`,count=shown.length===feeds.length?(feeds.length===1?'Showing 1 feed':`Showing all ${total}`):`Showing ${shown.length} of ${total}`;
 // The count is a live region; rewriting the same text every poll would
 // announce it again.
 if($('#feed-count').textContent!==count)$('#feed-count').textContent=count;
}
function feedCard(f){
 const status=feedStatus(f),held=status==='held',focusable=(element,key)=>{element.dataset.focus=key;return element;};
 const card=node('article',undefined,'feed-card'+(f.enabled?'':' paused'));card.dataset.feedId=f.id;
 const top=node('div',undefined,'feed-top');top.append(node('h2',f.title),node('span',statusLabels[status],'badge'+(status==='failing'||held?' error':status==='quiet'?' badge-quiet':'')));card.append(top);
 const source=node('a',f.url,'source');source.href=f.url;source.target='_blank';source.rel='noopener noreferrer';card.append(focusable(source,'source'));
 const times=node('dl',undefined,'times');for(const [label,value] of [['Saved items',f.count],['Next refresh',!f.enabled?'Paused':held?'Delayed after a failed save':when(f.next_run)],['Failures in a row',f.failures||0],['Last attempt',when(f.last_attempt)],['Last success',when(f.last_success)],['Last change',when(f.last_changed)]]){const pair=node('div');pair.append(node('dt',label),node('dd',String(value)));times.append(pair);}card.append(times);
 const libraryFilters=window.rssLibrary?.names(f.filter_ids||[])||[];
 if(libraryFilters.length)card.append(node('p',`Library filters: ${libraryFilters.join(', ')}`,'feed-filters'));
 if(held)card.append(node('p','Waiting to retry a failed database write. The last refresh could not be saved; it is retried after a delay, or now with Refresh.','diagnostic feed-held'));
 if(status==='quiet'){
  // As the server measures it: from the last new story, or from the last
  // output change of a feed that has stored none.
  const days=Math.max(0,Math.floor((Date.now()-(stamp(f.last_new)??stamp(f.last_changed)))/864e5));
  card.append(node('p',`No new story for ${days} day${days===1?'':'s'}. Refreshes keep succeeding, but none has found a story this feed did not already have. Check that the page still lists new stories, or preview the recipe from Edit.`,'feed-quiet'));
 }
 if(f.error)card.append(node('p',f.error,'diagnostic'));
 const actions=node('div',undefined,'feed-actions');actions.append(focusable(action('Edit',()=>openEditor(f)),'edit'),focusable(action('Duplicate',()=>openEditor(f,true)),'duplicate'),focusable(action('Refresh',async()=>{await api(`/feeds/${f.id}/refresh`,'POST',{});notice('Refresh queued.',true);await load();}),'refresh'),focusable(action(f.enabled?'Pause':'Resume',async()=>{await api(`/feeds/${f.id}/enabled`,'POST',{enabled:!f.enabled});await load();}),'enabled'),focusable(action('Delete',async()=>{if(!confirm(`Delete “${f.title}” and all its saved items? This cannot be undone.`))return;await api(`/feeds/${f.id}`,'DELETE',{});if(form.elements.id.value===f.id)$('#editor').hidden=true;await load();},'quiet danger'),'delete'));card.append(actions);
 const diagnostics=node('button','Diagnostics','quiet feed-diagnostics');diagnostics.type='button';diagnostics.onclick=()=>window.rssDiagnostics.open(f);diagnostics.dataset.feedId=f.id;actions.append(focusable(diagnostics,'diagnostics'));
 const exportLink=node('a','Export recipe','quiet button-link');exportLink.href='/api/recipes/export?id='+encodeURIComponent(f.id);exportLink.download='';actions.append(focusable(exportLink,'export'));
 const links=node('div',undefined,'feed-links');
 for(const [format,url] of [['RSS',f.rss_url],['Atom',f.atom_url]]){
  if(!url)continue;
  const row=node('div',undefined,'feed-format'),link=node('a',`Open ${format} feed ↗`,'rss-link');link.href=url;link.target='_blank';link.rel='noopener noreferrer';row.append(focusable(link,'open-'+format),focusable(action(`Copy ${format} URL`,()=>copyFeedURL(format,url)),'copy-'+format));links.append(row);
 }
 links.append(focusable(action('Reset feed links',async()=>{if(!confirm('Reset both RSS and Atom links? Readers using either old link will need the new one.'))return;await api(`/feeds/${f.id}/rotate-token`,'POST',{});await load();}),'reset-links'));card.append(links);
 return card;
}
$('#feed-search').oninput=()=>{view.search=$('#feed-search').value;renderLibrary();};
$('#feed-status').onchange=()=>{view.status=$('#feed-status').value;saveView();renderLibrary();};
$('#feed-sort').onchange=()=>{view.sort=$('#feed-sort').value;saveView();renderLibrary();};
$('#show-all-feeds').onclick=()=>{view.search='';view.status='all';$('#feed-search').value='';$('#feed-status').value='all';saveView();renderLibrary();$('#feed-search').focus();};
restoreView();
async function copyFeedURL(format,url){
 if(navigator.clipboard&&window.isSecureContext){try{await navigator.clipboard.writeText(url);notice(format+' URL copied.',true);return;}catch{}}
 window.prompt('Copy this '+format+' URL:',url);
}
// validate() allows a title of 200 bytes of UTF-8, while the field's
// maxlength counts UTF-16 units, so a copy's name is shortened by its encoded
// size, a whole character at a time.
function copyTitle(title,limit=200){
 const full='Copy of '+title,encoder=new TextEncoder(),size=v=>encoder.encode(v).length;
 if(size(full)<=limit)return full;
 const parts=typeof Intl.Segmenter==='function'?Array.from(new Intl.Segmenter(undefined,{granularity:'grapheme'}).segment(full),part=>part.segment):[...full];
 let out='',used=0;const room=limit-size('…');
 for(const part of parts){const n=size(part);if(used+n>room)break;out+=part;used+=n;}
 return out.trimEnd()+'…';
}
// The library filters a copy links, for a save made while the library could
// not be loaded: a new feed that omitted them would have none.
let copiedFilterIDs=null;
// copy opens a new, unsaved feed with f's settings. It has no id, so saving
// creates a feed with its own reader links, and it never offers to prune
// saved stories, because it has none.
function openEditor(f,copy=false){
 const editing=!!f&&!copy;
 form.reset();$('#preview').replaceChildren();$('#editor-title').textContent=copy?'Duplicate feed':editing?'Edit feed':'New feed';form.elements.id.value=editing?f.id:'';loadedEnabled=editing?f.enabled:null;
 copiedFilterIDs=copy?[...(f.filter_ids||[])]:null;$('#duplicate-hint').hidden=!copy;
 if(f){form.elements.mode.value=f.recipe.mode||"static";form.elements.wait_selector.value=f.recipe.wait_selector||"";form.elements.settle_ms.value=f.recipe.settle_ms||0;form.elements.title.value=copy?copyTitle(f.title):f.title;form.elements.url.value=f.url;form.elements.type.value=f.recipe.type;form.elements.items.value=f.recipe.items;form.elements.interval.value=f.interval/60;form.elements.enabled.checked=!copy&&f.enabled;for(const k of ['title','link','content','image','date']){form.elements[k+'_selector'].value=f.recipe[k].selector;form.elements[k+'_attr'].value=f.recipe[k].attr;}form.elements.date_layout.value=f.recipe.date_layout;form.elements.timezone.value=f.recipe.timezone;
  const full=f.recipe.full_content;form.elements.full_selector.value=full?.selector||'';form.elements.full_attr.value=full?.attr||'';form.elements.full_browser.checked=!!full?.browser;form.elements.user_agent.value=f.recipe.user_agent||'';form.elements.accept_language.value=f.recipe.accept_language||'';form.elements.quiet_days.value=String(f.recipe.quiet_days||0);form.elements.cleanup.value=(f.recipe.remove||[]).join('\n');}
 // A saved feed keeps its source: a web page and a feed identify stories
 // differently, so switching would publish every story again. A copy can
 // choose either, and a feed source shows the selector type as CSS.
 const feedSource=f?.recipe.type==='feed';form.elements.source.value=feedSource?'feed':'page';form.elements.source.disabled=editing;if(feedSource)form.elements.type.value='css';renderSource();
 window.rssFilters?.load(f?.recipe.filters,editing);window.rssLibrary?.loadFeed(f?.filter_ids);window.rssLibrary?.refresh().catch(()=>{});$('#editor').hidden=false;renderHelp();help();fullContentSummary();requestSummary();cleanupSummary();form.elements.title.focus();
}
function readForm(){const v=new FormData(form);const recipe={mode:v.get('mode'),wait_selector:v.get('wait_selector'),settle_ms:Number(v.get('settle_ms')),type:v.get('type'),items:v.get('items'),date_layout:v.get('date_layout'),timezone:v.get('timezone')};for(const k of ['title','link','content','image','date'])recipe[k]={selector:v.get(k+'_selector'),attr:v.get(k+'_attr')};const filters=window.rssFilters?.read();if(filters)recipe.filters=filters;
 const fullSelector=(v.get('full_selector')||'').trim();
 if(fullSelector)recipe.full_content={selector:fullSelector,attr:v.get('full_attr')||'',browser:form.elements.full_browser.checked};
 // A feed source's entries supply every field, and it is fetched as Static
 // HTTP with plain-HTML articles, so the page settings it hides are not sent.
 if(form.elements.source.value==='feed'){Object.assign(recipe,{mode:'static',wait_selector:'',settle_ms:0,type:'feed',items:''});for(const k of ['title','link','content','image','date'])recipe[k]={selector:'',attr:''};if(recipe.full_content)recipe.full_content.browser=false;}
 // Blank is left out, as the server stores it, so the server's default
 // User-Agent applies and no Accept-Language is sent.
 const userAgent=(v.get('user_agent')||'').trim();if(userAgent)recipe.user_agent=userAgent;
 const language=(v.get('accept_language')||'').trim();if(language)recipe.accept_language=language;
 // Automatic is left out, as the server stores it.
 const quietDays=Number(v.get('quiet_days')||0);if(quietDays)recipe.quiet_days=quietDays;
 // One rule per line; blank lines are not rules, and no rules is left out.
 const remove=cleanupRules();if(remove.length)recipe.remove=remove;
 const out={title:v.get('title'),url:v.get('url'),interval:Number(v.get('interval'))*60,enabled:form.elements.enabled.checked,recipe};
 // Omitted while the library is unavailable, which keeps the feed's current
 // filters; a copy sends the ones it was opened with.
 const libraryFilters=window.rssLibrary?.selected()??copiedFilterIDs;if(libraryFilters)out.filter_ids=libraryFilters;
 return out;}
function fullContentSummary(){
 const on=(form.elements.full_selector?.value||'').trim();
 const label=$('#full-summary');if(label)label.textContent=on?'On':'Off';
}
function cleanupRules(){return (form.elements.cleanup?.value||'').split(/\r\n|\r|\n/).map(rule=>rule.trim()).filter(Boolean);}
function cleanupSummary(){
 const n=cleanupRules().length,label=$('#cleanup-summary');
 if(label)label.textContent=n?`${n} rule${n===1?'':'s'}`:'Off';
}
function requestSummary(){
 const own=(form.elements.user_agent?.value||'').trim(), language=(form.elements.accept_language?.value||'').trim();
 const label=$('#request-summary');if(label)label.textContent=(own?'Custom User-Agent':'Default User-Agent')+(language?', custom Accept-Language':'');
}
function renderSource(){
 const feed=form.elements.source.value==='feed';
 $('#page-selectors').hidden=feed;form.elements.type.closest('label').hidden=feed;form.elements.mode.closest('label').hidden=feed;$('#fetch-mode-help').hidden=feed;
 // Hidden fields cannot be filled in, so they stop being required.
 form.elements.items.required=form.elements.title_selector.required=!feed;
 $('#url-label').textContent=feed?'Feed URL':'Page URL';form.elements.url.placeholder=feed?'https://example.com/feed.xml':'https://example.com/news';
 $('#full-render').hidden=feed;$('#full-feed-hint').hidden=!feed;
 // Hints written for one source, such as the cleanup section's.
 for(const hint of form.querySelectorAll('[data-source]'))hint.hidden=hint.dataset.source!==(feed?'feed':'page');
 $('#source-help').textContent=(feed?'Reads the stories of an existing RSS or Atom feed over Static HTTP, to filter them or add their full articles. The feed supplies each story’s title, link, date, description and image, so there are no selectors.':'Selects the stories on a web page with CSS or XPath.')+(form.elements.source.disabled?' A saved feed keeps its source; to try the other, duplicate it.':'');
 renderHelp();help();
}
function renderHelp(){
 const mode=form.elements.mode.value, localBrowser=form.elements.source.value!=='feed'&&(mode==='browser'||mode==='auto'), tip=$('#fetch-mode-help');
 $('#render-settings').hidden=!localBrowser;
 tip.classList.toggle('diagnostic',localBrowser&&!browserAvailable||mode==='flaresolverr'&&!flaresolverrAvailable);
 if(mode==='flaresolverr')tip.textContent=flaresolverrAvailable?'Uses the configured external browser for Cloudflare challenge pages. Some CAPTCHAs remain unsupported.':'FlareSolverr is not configured on this server. Set FLARESOLVERR_URL to an address the server can reach. With Docker Compose, put it in .env and recreate the container with docker compose up -d and the same -f files; docker compose restart keeps the old settings. For a native run, export it and start the server again. See docs/flaresolverr.md.';
 else if(localBrowser&&!browserAvailable)tip.textContent='Chromium is not configured on this server. Enable the browser deployment before using this mode.';
 else if(mode==='auto')tip.textContent='Tries static HTML first, then Chromium if no items are found. Access blocks do not trigger a retry.';
 else tip.textContent=mode==='browser'?'Renders the page with the server’s Chromium browser before extracting items.':'Fetches the page’s HTML without running JavaScript.';
}
form.elements.mode.onchange=renderHelp;
function help(){
 // A feed source's rules are CSS, whatever the hidden selector type says.
 const xpath=form.elements.source.value!=='feed'&&form.elements.type.value==='xpath';
 $('#selector-help').textContent=xpath?'Use relative XPath fields starting with ., such as .//h2 or .//a/@href. Use . for the card itself.':'Fields are relative to each card. Use . for the card itself. Choose an attribute separately, such as href or datetime.';
 $('#cleanup-syntax-help').textContent=xpath?'XPath rules start with ./, such as .//div[@class=\'share\'] or .//p[contains(., \'appeared first on\')].':'For example .share, aside.related or p:containsOwn("appeared first on"), which ignores case.';
}
$('#login-form').onsubmit=e=>{e.preventDefault();busy(e.submitter,async()=>{const out=await api('/login','POST',{password:$('#password').value});csrf=out.csrf;$('#password').value='';await showApp();});};
$('#logout').onclick=()=>busy($('#logout'),async()=>{await api('/logout','POST',{});showLogin();});
$('#new-feed').onclick=$('#empty-new').onclick=()=>openEditor();$('#close-editor').onclick=()=>$('#editor').hidden=true;form.elements.type.onchange=help;
form.elements.full_selector.oninput=fullContentSummary;form.elements.cleanup.oninput=cleanupSummary;
form.elements.source.onchange=renderSource;
form.elements.user_agent.oninput=form.elements.accept_language.oninput=requestSummary;
form.onsubmit=e=>{e.preventDefault();busy(e.submitter,async()=>{const id=form.elements.id.value,data=readForm();if(id){data.apply_filters_to_history=$('#apply-filters-to-history').checked;if(typeof loadedEnabled==='boolean')data.loaded_enabled=loadedEnabled;}await api(id?`/feeds/${id}`:'/feeds',id?'PUT':'POST',data);$('#editor').hidden=true;notice('Feed saved. Enabled feeds refresh in the background.',true);await load();});};
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
   // Show the fetched article body when there is one: it is what the reader
   // will receive, and a selector that matches the wrong block succeeds
   // silently otherwise. Previews only fetch the first few articles.
   const body=item.full_html||item.html;
   if(item.full_html)card.append(node('p','Article body from this story\u2019s own page','meta'));
   const content=node('div');content.innerHTML=body;for(const a of content.querySelectorAll('a')){a.target='_blank';a.rel='noopener noreferrer';}card.append(content);p.append(card);
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
