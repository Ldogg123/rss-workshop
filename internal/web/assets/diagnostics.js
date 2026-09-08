'use strict';
(() => {
 const dialog=$('#feed-diagnostics'), rows=$('#diagnostics-runs'), status=$('#diagnostics-status'), reload=$('#diagnostics-reload');
 const modes={static:'Static HTML',browser:'Chromium',auto:'Auto',flaresolverr:'FlareSolverr'};
 const number=value=>Number.isSafeInteger(value)&&value>=0?value:null;
 const text=(value,limit=1000)=>typeof value==='string'?value.slice(0,limit):'';
 let revision=0, controller=null, selected=null;

 // Keep only the bounded, public diagnostic fields. Rendering below always
 // uses textContent, including source-derived error and warning strings.
 function normalize(value){
  if(!value||value.version!==1||typeof value.requested_mode!=='string'||!Object.hasOwn(modes,value.requested_mode)||!['css','xpath'].includes(value.selector_type)||!Array.isArray(value.attempts))return null;
  const attempts=[];
  for(const attempt of value.attempts.slice(0,2)){
   if(!attempt||!['static','browser','flaresolverr'].includes(attempt.mode)||!['success','failed','not_modified'].includes(attempt.outcome)||!['fetch','extract'].includes(attempt.stage))continue;
   attempts.push({mode:attempt.mode,outcome:attempt.outcome,stage:attempt.stage,status:number(attempt.status),duration_ms:number(attempt.duration_ms),bytes:number(attempt.bytes),matches:number(attempt.matches),valid:number(attempt.valid),filtered:number(attempt.filtered)||0,items:number(attempt.items),error:text(attempt.error),warnings:Array.isArray(attempt.warnings)?attempt.warnings.filter(w=>typeof w==='string').slice(0,20).map(w=>text(w,512)):[],warnings_omitted:number(attempt.warnings_omitted)||0});
  }
  return {version:1,recipe_version:number(value.recipe_version),requested_mode:value.requested_mode,selector_type:value.selector_type,started:text(value.started,64),duration_ms:number(value.duration_ms),attempts};
 }
 function elapsed(value){return value===null?'Not recorded':value<1000?`${value} ms`:`${(value/1000).toFixed(1)} s`;}
 function bytes(value){return value<1024?`${value} B`:value<1024*1024?`${(value/1024).toFixed(1)} KiB`:`${(value/(1024*1024)).toFixed(1)} MiB`;}
 function date(value){return typeof value==='string'&&Number.isFinite(Date.parse(value))?when(value):'Time not recorded';}
 function metrics(pairs){
  const list=node('dl',undefined,'run-metrics');
  for(const [label,value] of pairs){const pair=node('div');pair.append(node('dt',label),node('dd',String(value)));list.append(pair);}
  return list;
 }
 function renderTrace(trace){
  const body=node('div',undefined,'run-trace');
  const pairs=[['Fetch mode',modes[trace.requested_mode]],['Selectors',trace.selector_type==='xpath'?'XPath':'CSS'],['Started',date(trace.started)],['Total time',elapsed(trace.duration_ms)]];
  if(trace.recipe_version>0)pairs.push(['Recipe version',trace.recipe_version]);
  body.append(metrics(pairs));
  if(trace.requested_mode==='auto'&&trace.attempts.length>1)body.append(node('p','Auto tried static HTML, then Chromium.','hint'));
  if(!trace.attempts.length)body.append(node('p','No fetch attempt was recorded.','hint'));
  const attempts=node('ol',undefined,'run-attempts');
  for(const attempt of trace.attempts){
   const item=node('li',undefined,'run-attempt');
   const outcome=attempt.outcome==='not_modified'?'Page unchanged':attempt.outcome==='success'?'Extraction completed':attempt.stage==='fetch'?'Fetch failed':'Matching failed';
   item.append(node('h3',`${modes[attempt.mode]} · ${outcome}`));
   const details=[['Source response',attempt.status>0?`HTTP ${attempt.status}`:'No HTTP status recorded'],['Time',elapsed(attempt.duration_ms)]];
   if(attempt.bytes!==null)details.push(['Received',bytes(attempt.bytes)]);
   if(attempt.matches!==null)details.push(['Matched elements',attempt.matches]);
   if(attempt.valid!==null){details.push(['Valid before filters',attempt.valid]);if(attempt.items!==null)details.push(['Included items',attempt.items]);details.push(['Filtered out',attempt.filtered]);}
   else if(attempt.items!==null)details.push(['Valid items',attempt.items]);
   item.append(metrics(details));
   if(attempt.outcome==='not_modified')item.append(node('p','The source reported no changes. Previously saved items were kept.','hint'));
   if(attempt.error)item.append(node('p',attempt.error,'run-error'));
   if(attempt.warnings.length){const warnings=node('ul',undefined,'run-warnings');for(const warning of attempt.warnings)warnings.append(node('li',warning,'run-warning'));item.append(warnings);}
   if(attempt.warnings_omitted)item.append(node('p',`${attempt.warnings_omitted} more warning${attempt.warnings_omitted===1?' was':'s were'} omitted.`,'hint'));
   attempts.append(item);
  }
  body.append(attempts);return body;
 }
 function appendPreview(parent,value){
  const trace=normalize(value);if(!trace)return;
  const details=node('details',undefined,'preview-diagnostics');
  details.append(node('summary','Preview diagnostics'),node('p','Details from this preview. Previews are not added to the saved refresh history.','hint'),renderTrace(trace));parent.append(details);
 }
 function renderRun(run){
  const trace=normalize(run.diagnostics),error=text(run.error),last=trace?.attempts.at(-1);
  const failed=!!error||last?.outcome==='failed'||number(run.status)>=400;
  const unchanged=!failed&&(last?.outcome==='not_modified'||run.status===304);
  const item=node('details',undefined,'run-entry');item.dataset.runId=String(number(run.id)||0);
  const summary=node('summary',undefined,'run-summary');
  summary.append(node('span',failed?'Failed':unchanged?'Unchanged':'Completed','badge'+(failed?' error':'')),node('span',date(run.ended),'run-time'));
  if(trace)summary.append(node('span',modes[trace.requested_mode],'meta'));
  if(unchanged)summary.append(node('span','Saved items kept','meta'));
  else if(number(run.count)!==null)summary.append(node('span',`${run.count} ${number(last?.valid)!==null?'included':'valid'} item${run.count===1?'':'s'}`,'meta'));
  item.append(summary);
  if(error)item.append(node('p',error,'run-error'));
  if(trace)item.append(renderTrace(trace));
  else{
   if(number(run.status)>0)item.append(node('p',`Source response: HTTP ${run.status}`,'meta'));
   item.append(node('p','Detailed diagnostics are unavailable for this older run.','hint'));
  }
  return item;
 }
 function reset(){
  revision++;controller?.abort();controller=null;selected=null;
  rows.replaceChildren();rows.setAttribute('aria-busy','false');status.textContent='';status.classList.remove('diagnostic');$('#diagnostics-feed').textContent='';reload.disabled=false;
 }
 function dismiss(){const id=selected?.id;reset();if(dialog.open)dialog.close();if(id&&csrf)document.querySelector(`.feed-diagnostics[data-feed-id="${CSS.escape(id)}"]`)?.focus();}
 function current(version){return version===revision&&dialog.open&&selected!==null;}
 async function readRuns(){
  if(!selected)return;
  controller?.abort();controller=new AbortController();const version=++revision,id=selected.id;
  rows.replaceChildren();rows.setAttribute('aria-busy','true');status.classList.remove('diagnostic');status.textContent='Loading refresh history…';reload.disabled=true;
  try{
   const out=await api(`/feeds/${encodeURIComponent(id)}/runs`,'GET',undefined,controller.signal);
   if(!current(version))return;
   if(!Array.isArray(out.runs))throw new Error('Refresh history could not be read. Try reloading it.');
   const runs=out.runs.filter(run=>run&&typeof run==='object').slice(0,50);
   for(const run of runs)rows.append(renderRun(run));
   status.textContent=runs.length?`${runs.length} recent refresh run${runs.length===1?'':'s'}. Newest first.`:'No refresh runs yet. A run will appear after this feed is refreshed.';
  }catch(error){
   if(!current(version)||error.name==='AbortError')return;
   status.classList.add('diagnostic');status.textContent=error.status===404?'This feed was deleted or is no longer available.':error.message;
   if(error.status===404)selected=null;
  }finally{
   if(version===revision&&dialog.open){controller=null;rows.setAttribute('aria-busy','false');reload.disabled=selected===null;}
  }
 }
 function open(feed){
  reset();selected={id:String(feed.id),title:text(feed.title,500)};$('#diagnostics-feed').textContent=selected.title;
  if(!dialog.open)dialog.showModal();$('#diagnostics-close').focus();readRuns();
 }
 reload.onclick=readRuns;$('#diagnostics-close').onclick=dismiss;
 dialog.addEventListener('cancel',event=>{event.preventDefault();dismiss();});
 dialog.addEventListener('close',()=>{if(!dialog.open)reset();});
 window.addEventListener('rss-session-ended',()=>{dismiss();for(const details of document.querySelectorAll('.preview-diagnostics'))details.remove();});
 window.addEventListener('rss-feeds-loaded',event=>{if(selected&&Array.isArray(event.detail)&&!event.detail.includes(selected.id)){dismiss();notice('This feed is no longer available.');}});
 window.rssDiagnostics={normalize,appendPreview,open};
})();
