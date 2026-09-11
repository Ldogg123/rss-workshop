'use strict';
(() => {
 const section=$('#story-filters'), panels={include:$('#filter-include'),exclude:$('#filter-exclude')};
 const count=$('#filter-count'), error=$('#filter-error');
 // Bounds are rendered by the server from internal/filter so this editor
 // always matches the validator that rejects oversized filters on save.
 const max={keywords:+count.dataset.maxKeywords,nodes:+count.dataset.maxNodes,depth:+count.dataset.maxDepth,characters:+count.dataset.maxKeywordCharacters};
 let roots={}, nextID=0;
 const leaf=()=>({op:'contains_any',field:'title',keywords:[],_text:'',_id:++nextID});
 const group=rules=>({op:'all',rules,_open:true,_id:++nextID});
 const isGroup=rule=>rule.op==='all'||rule.op==='any';
 const phrase=term=>term.replace(/\p{White_Space}+/gu,' ').replace(/^ | $/g,'');
 const phrases=value=>value.split(/\r?\n/).map(phrase).filter(Boolean);
 function copy(rule){
  if(!rule)return null;
  if(isGroup(rule))return {op:rule.op,rules:rule.rules.map(copy),_open:true,_id:++nextID};
  return {op:rule.op,field:rule.field,keywords:[...rule.keywords],_text:rule.keywords.map(phrase).join('\n'),_id:++nextID};
 }
 function totals(){
  let keywords=0,rules=0;
  function visit(rule){if(!rule)return;rules++;if(isGroup(rule))rule.rules.forEach(visit);else keywords+=phrases(rule._text).length;}
  visit(roots.include);visit(roots.exclude);return {keywords,rules};
 }
 function update(){
  const n=totals();count.textContent=`${n.keywords} / ${max.keywords} phrases · ${n.rules} / ${max.nodes} rules`;
  count.classList.toggle('diagnostic',n.keywords>max.keywords||n.rules>max.nodes);
  error.hidden=true;error.textContent='';
  $('#filter-summary-count').textContent=n.rules?`${n.keywords} phrase${n.keywords===1?'':'s'}`:'Optional';
 }
 function button(label,callback,cls='quiet'){
  const b=node('button',label,cls);b.type='button';b.onclick=callback;return b;
 }
 function select(label,values,value,change,cls){
  const wrap=node('label',label),input=node('select');if(cls)input.className=cls;
  for(const [key,title] of values){const option=node('option',title);option.value=key;input.append(option);}
  input.value=value;input.onchange=()=>{change(input.value);update();};wrap.append(input);return wrap;
 }
 function focusRule(rule){section.querySelector(`[data-filter-id="${rule._id}"] textarea, [data-filter-id="${rule._id}"] select`)?.focus();}
 function renderRule(rule,remove,depth){
  const item=node(isGroup(rule)?'details':'div',undefined,isGroup(rule)?'filter-group':'filter-condition');item.dataset.filterId=rule._id;
  if(isGroup(rule)){
   item.open=rule._open;item.ontoggle=()=>rule._open=item.open;
   const summary=node('summary',`${rule.op==='all'?'All':'Any'} conditions · ${rule.rules.length} rule${rule.rules.length===1?'':'s'}`);item.append(summary);
   const controls=node('div',undefined,'filter-rule-heading');
   controls.append(select('Match',[['all','All conditions (AND)'],['any','Any condition (OR)']],rule.op,value=>{rule.op=value;summary.textContent=`${value==='all'?'All':'Any'} conditions · ${rule.rules.length} rule${rule.rules.length===1?'':'s'}`;},'filter-group-op'),button('Remove group',remove,'quiet danger filter-remove'));
   item.append(controls);
   const children=node('div',undefined,'filter-children');
   for(const child of rule.rules)children.append(renderRule(child,()=>{rule.rules.splice(rule.rules.indexOf(child),1);render();},depth+1));
   item.append(children);
   const actions=node('div',undefined,'filter-actions');
   const addCondition=button('+ Condition',()=>{const added=leaf();rule.rules.push(added);rule._open=true;render();focusRule(added);},'quiet filter-add-condition');
   const addGroup=button('+ Group',()=>{const added=group([leaf()]);rule.rules.push(added);rule._open=true;render();focusRule(added);},'quiet filter-add-group');
   addCondition.disabled=depth>=max.depth;addGroup.disabled=depth>=max.depth-1;
   if(depth>=max.depth-1)addGroup.title=`Groups can nest up to ${max.depth} levels, including their conditions.`;
   actions.append(addCondition,addGroup);item.append(actions);
   if(!rule.rules.length)item.append(node('p','Add a condition, or remove this empty group.','hint'));
  }else{
   const controls=node('div',undefined,'filter-rule-heading');
   controls.append(select('Field',[['title','Title'],['description','Description'],['link','Link']],rule.field,value=>rule.field=value,'filter-field'),button('Remove',remove,'quiet danger filter-remove'));
   item.append(controls,select('Matching',[['contains_any','Contains any phrase'],['contains_all','Contains every phrase']],rule.op,value=>rule.op=value,'filter-condition-op'));
   const label=node('label','Phrases — one per line'),textarea=node('textarea');textarea.className='filter-keywords';textarea.rows=5;textarea.placeholder='climate change\nrenewable energy\npublic transport';textarea.value=rule._text;
   const hint=node('p',`${phrases(rule._text).length} phrases`,'hint filter-phrase-count');
   textarea.oninput=()=>{rule._text=textarea.value;hint.textContent=`${phrases(rule._text).length} phrases`;update();};
   label.append(textarea);item.append(label,hint);
  }
  return item;
 }
 function render(){
  for(const kind of ['include','exclude']){
   const panel=panels[kind];panel.replaceChildren();
   if(roots[kind])panel.append(renderRule(roots[kind],()=>{delete roots[kind];render();},1));
   else panel.append(node('p',kind==='include'?'All valid stories are eligible. Add conditions to require a match.':'No stories are excluded. Add conditions to remove unwanted matches.','hint filter-empty'));
   if(roots[kind]&&isGroup(roots[kind]))continue;
   const actions=node('div',undefined,'filter-actions');
   for(const [label,make,cls] of [['+ Condition',leaf,'filter-add-condition'],['+ Group',()=>group([leaf()]),'filter-add-group']]){
    actions.append(button(label,()=>{const added=make();if(!roots[kind])roots[kind]=added;else if(isGroup(roots[kind]))roots[kind].rules.push(added);else roots[kind]=group([roots[kind],added]);render();focusRule(added);},`quiet ${cls}`));
   }
   panel.append(actions);
  }
  update();
 }
 function load(filters,editing){
  roots={};nextID=0;for(const kind of ['include','exclude'])if(filters?.[kind])roots[kind]=copy(filters[kind]);
  section.open=!!(roots.include||roots.exclude);$('#apply-filter-history').hidden=!editing;
  $('#apply-filters-to-history').checked=false;render();
 }
 function read(){
  let nodes=0,keywords=0;
  function visit(rule,depth){
   if(++nodes>max.nodes)throw new Error(`Use at most ${max.nodes} filter rules across both panels.`);
   if(depth>max.depth)throw new Error(`Filter groups can nest up to ${max.depth} levels, including their conditions.`);
   if(isGroup(rule)){
    if(!rule.rules.length)throw new Error('Add a condition to each filter group, or remove the empty group.');
    return {op:rule.op,rules:rule.rules.map(child=>visit(child,depth+1))};
   }
   const terms=phrases(rule._text);
   if(!terms.length)throw new Error('Enter at least one phrase for each condition, or remove the empty condition.');
   if(terms.some(term=>[...term].length>max.characters))throw new Error(`Each filter phrase must be at most ${max.characters} characters.`);
   keywords+=terms.length;if(keywords>max.keywords)throw new Error(`Use at most ${max.keywords} filter phrases across both panels.`);
   return {op:rule.op,field:rule.field,keywords:terms};
  }
  try{
   const out={};for(const kind of ['include','exclude'])if(roots[kind])out[kind]=visit(roots[kind],1);
   error.hidden=true;return Object.keys(out).length?out:undefined;
  }catch(e){section.open=true;error.textContent=e.message;error.hidden=false;throw e;}
 }
 function preview(parent,out,failed=false){
  const included=out.items.length,filtered=Number.isSafeInteger(out.filtered)&&out.filtered>=0?out.filtered:0;
  const valid=Number.isSafeInteger(out.valid)&&out.valid>=0?out.valid:included+filtered;
  parent.append(node('p',failed?`Processed before the error: ${valid} valid · ${filtered} filtered out`:`${valid} valid before filters · ${included} included · ${filtered} filtered out`,'hint filter-preview-counts'));
  if(!failed&&filtered&&included===0)parent.append(node('p',`All ${valid} valid stories were filtered out. Extraction succeeded; no new stories will be added.`,'filter-empty-result'));
  const examples=Array.isArray(out.filter_examples)?out.filter_examples.filter(example=>example&&typeof example.title==='string'&&typeof example.reason==='string').slice(0,20):[];
  if(!examples.length)return;
  const details=node('details',undefined,'filter-examples');details.append(node('summary',`Why stories were filtered out (${examples.length}${filtered>examples.length?` of ${filtered}`:''})`));
  const list=node('ul');for(const example of examples){const row=node('li');row.append(node('strong',example.title.slice(0,240)||'Untitled story'),node('p',example.reason.slice(0,512)));list.append(row);}details.append(list);parent.append(details);
 }
 window.rssFilters={load,read,preview};load(undefined,false);
})();
