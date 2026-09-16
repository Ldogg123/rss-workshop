'use strict';
(() => {
 const dialog=$('#filter-library'), list=$('#filter-library-list'), status=$('#filter-library-status'), browse=$('#filter-library-browse');
 const form=$('#library-filter-form'), name=$('#library-filter-name'), usedBy=$('#library-filter-used-by'), history=$('#library-filter-history'), applyHistory=$('#library-filter-apply-history');
 const choices=$('#library-filter-choices'), empty=$('#filter-library-empty');
 const editor=window.rssFilters.editor({section:form,include:$('#library-filter-include'),exclude:$('#library-filter-exclude'),count:$('#library-filter-count'),error:$('#library-filter-error')});
 // filters stays null until the library has loaded. Until then the feed
 // editor omits filter_ids, which keeps a feed's current library filters
 // rather than unlinking everything it could not display.
 let filters=null, selected=[], editing=null, saves=0;
 const plural=(n,word)=>`${n} ${word}${n===1?'':'s'}`;
 const titles=ids=>ids.map(id=>feeds.find(f=>f.id===id)?.title).filter(Boolean);
 function describe(filter){
  const parts=[plural(window.rssFilters.phrases(filter.filters),'phrase')];
  if(filter.filters.include)parts.push('include');if(filter.filters.exclude)parts.push('exclude');
  return parts.join(' · ');
 }
 function message(text,failed=false){status.textContent=text;status.classList.toggle('diagnostic',failed);}
 // The page notice sits behind this modal dialog, so report failures inside it.
 function act(label,fn,cls){return action(label,async()=>{try{await fn();}catch(e){message(e.message,true);}},cls);}
 async function refresh(){
  const out=await api('/filters');filters=Array.isArray(out.filters)?out.filters:[];
  selected=selected.filter(id=>filters.some(f=>f.id===id));
  renderList();renderChoices();
 }
 function renderList(){
  list.replaceChildren();empty.hidden=!!filters?.length;
  for(const filter of filters){
   const item=node('li',undefined,'library-filter'),text=node('div');
   const users=titles(filter.feed_ids);
   text.append(node('h3',filter.name),node('p',describe(filter),'meta'),node('p',filter.feed_ids.length?`Used by ${users.join(', ')||plural(filter.feed_ids.length,'feed')}`:'Not used by any feed','meta'));
   const actions=node('div',undefined,'feed-actions');
   actions.append(act('Edit',async()=>open(filter)));
   const remove=act('Delete',async()=>{
    if(!confirm(`Delete the library filter “${filter.name}”?`))return;
    await api(`/filters/${encodeURIComponent(filter.id)}`,'DELETE',{});await refresh();message('Filter deleted.');
   },'quiet danger');
   if(filter.feed_ids.length){remove.disabled=true;remove.title='Remove this filter from every feed using it before deleting it.';}
   actions.append(remove);item.append(text,actions);list.append(item);
  }
 }
 function open(filter){
  saves++;editing=filter||null;browse.hidden=true;form.hidden=false;message('');
  $('#library-filter-form-title').textContent=filter?'Edit filter':'New filter';
  name.value=filter?.name||'';editor.load(filter?.filters);
  const users=filter?titles(filter.feed_ids):[];
  usedBy.hidden=!filter?.feed_ids.length;usedBy.textContent=filter?.feed_ids.length?`Saving updates ${plural(filter.feed_ids.length,'feed')}: ${users.join(', ')}.`:'';
  history.hidden=!filter?.feed_ids.length;applyHistory.checked=false;name.focus();
 }
 function close(){saves++;editing=null;form.hidden=true;browse.hidden=false;}
 form.onsubmit=e=>{e.preventDefault();busy(e.submitter,async()=>{
  let rules;
  try{rules=editor.read();}catch{return;}
  if(!rules){$('#library-filter-error').textContent='Add at least one include or exclude rule.';$('#library-filter-error').hidden=false;return;}
  // A history cleanup can take seconds; the operator may cancel or open another
  // filter meanwhile, so the result only touches the form it was saved from.
  const target=editing,session=++saves,data={name:name.value,filters:rules};
  if(target)data.apply_filters_to_history=applyHistory.checked;
  try{
   await api(target?`/filters/${encodeURIComponent(target.id)}`:'/filters',target?'PUT':'POST',data);
  }catch(e){message(session===saves&&!form.hidden?e.message:`“${data.name}” was not saved: ${e.message}`,true);return;}
  const updated=target?.feed_ids.length||0;
  if(session===saves&&!form.hidden)close();
  message(updated?`Filter saved. ${plural(updated,'feed')} will use the new rules from their next refresh.`:'Filter saved.');
  try{await refresh();if(updated)await load();}catch(e){message(`Filter saved, but the list could not be reloaded: ${e.message}`,true);}
 });};
 $('#library-filter-cancel').onclick=close;
 $('#library-filter-new').onclick=()=>open();
 $('#filter-library-close').onclick=()=>dialog.close();
 async function show(){
  close();message('');if(!dialog.open)dialog.showModal();
  try{await refresh();}catch(e){message(e.message,true);}
 }
 $('#manage-filters').onclick=show;$('#library-filter-manage').onclick=show;

 // Feed editor selection.
 function renderChoices(){
  choices.replaceChildren();
  if(filters===null){choices.append(node('p','The filter library could not be loaded. Saving keeps this feed’s current library filters.','hint'));window.rssFilters.summarize();return;}
  if(!filters.length)choices.append(node('p','No library filters yet.','hint'));
  for(const filter of filters){
   const label=node('label',undefined,'check'),input=node('input');input.type='checkbox';input.value=filter.id;input.checked=selected.includes(filter.id);
   input.onchange=()=>{selected=input.checked?[...selected,filter.id]:selected.filter(id=>id!==filter.id);window.rssFilters.summarize();};
   label.append(input,node('span',filter.name),node('span',describe(filter),'meta'));choices.append(label);
  }
  window.rssFilters.summarize();
 }
 function loadFeed(ids){
  selected=Array.isArray(ids)?[...ids]:[];renderChoices();
  if(selected.length)$('#story-filters').open=true;
 }
 window.rssLibrary={
  refresh,loadFeed,
  selected:()=>filters===null?undefined:[...selected],
  selectedCount:()=>selected.length,
  names:ids=>filters===null?[]:ids.map(id=>filters.find(f=>f.id===id)?.name).filter(Boolean),
 };
 // Keep the selection: the feed editor stays open behind the sign-in form, and
 // clearing it would unlink every library filter on the next save.
 window.addEventListener('rss-session-ended',()=>{dialog.close();filters=null;});
})();
