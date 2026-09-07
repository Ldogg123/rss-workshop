'use strict';
(() => {
 const dialog=$('#recipe-import'), fileInput=$('#import-file'), status=$('#import-status'), preview=$('#import-preview'), confirmButton=$('#import-confirm'), closeButton=$('#import-close');
 const maxBytes=2*1024*1024;
 let revision=0, controller=null, candidate=null, importing=false;

 function clear(){
  revision++;controller?.abort();controller=null;candidate=null;
  preview.replaceChildren();preview.hidden=true;confirmButton.hidden=true;confirmButton.disabled=true;
  status.textContent='';status.classList.remove('diagnostic');
 }
 function current(version){return version===revision&&dialog.open;}
 function failure(message){status.textContent=message;status.classList.add('diagnostic');}
 function schedule(seconds){
  if(seconds%86400===0)return `Every ${seconds/86400} day${seconds===86400?'':'s'}`;
  if(seconds%3600===0)return `Every ${seconds/3600} hour${seconds===3600?'':'s'}`;
  return `Every ${seconds/60} minute${seconds===60?'':'s'}`;
 }
 function showPreview(items){
  for(const feed of items){
   const item=node('li',undefined,'import-recipe');
   item.append(node('h3',feed.title),node('p',feed.url,'source'));
   const mode={static:'Static HTTP',auto:'Auto',browser:'Chromium',flaresolverr:'FlareSolverr (Cloudflare)'}[feed.recipe.mode||'static']||feed.recipe.mode;
   item.append(node('p',`${schedule(feed.interval)} · ${mode}`,'meta'));
   const selectors=node('details'),summary=node('summary','Recipe selectors');selectors.append(summary);
   const mapping=node('dl',undefined,'import-mapping');
   for(const [label,value] of [['Items',`${feed.recipe.type.toUpperCase()}: ${feed.recipe.items}`],...['title','link','content','image','date'].map(field=>{
    const rule=feed.recipe[field],name={title:'Title',link:'Link',content:'Description',image:'Image',date:'Date'}[field];
    return [name,rule?.selector?rule.selector+(rule.attr?' → '+rule.attr:''):'Not specified'];
   })]){mapping.append(node('dt',label),node('dd',value));}
   selectors.append(mapping);item.append(selectors);preview.append(item);
  }
  preview.hidden=false;
 }
 async function validate(){
  clear();const version=revision,file=fileInput.files[0];if(!file)return;
  if(file.size>maxBytes){failure('This file is larger than 2 MiB. Choose a smaller export, or export individual recipes.');return;}
  status.textContent='Checking recipes…';controller=new AbortController();
  try{
   const raw=await file.text();if(!current(version))return;
   let document;try{document=JSON.parse(raw);}catch{throw new Error('This file is not valid JSON. Choose a recipe export from RSS Workshop.');}
   const out=await api('/recipes/preview','POST',document,controller.signal);
   if(!current(version))return;
   candidate=document;showPreview(out.feeds);
   status.textContent=out.count===0?'This file contains no recipes.':`${out.count} recipe${out.count===1?' is':'s are'} ready to import. Resume each feed when you are ready to fetch its items.`;
   confirmButton.textContent=`Import ${out.count} paused feed${out.count===1?'':'s'}`;
   confirmButton.hidden=out.count===0;confirmButton.disabled=out.count===0;
  }catch(e){if(current(version)&&e.name!=='AbortError')failure(e.message);}
  finally{if(current(version))controller=null;}
 }
 $('#import-recipes').onclick=()=>{clear();fileInput.value='';dialog.showModal();fileInput.focus();};
 fileInput.onchange=validate;
 closeButton.onclick=()=>{if(!importing)dialog.close();};
 dialog.addEventListener('cancel',e=>{if(importing)e.preventDefault();});
 dialog.addEventListener('close',()=>{clear();fileInput.value='';});
 window.addEventListener('rss-session-ended',()=>{if(dialog.open)dialog.close();});
 confirmButton.onclick=async()=>{
  if(!candidate||importing)return;
  const version=revision,document=candidate;
  importing=true;fileInput.disabled=true;closeButton.disabled=true;confirmButton.disabled=true;
  status.classList.remove('diagnostic');status.textContent='Importing paused feeds…';
  try{
   const out=await api('/recipes/import','POST',document);
   if(!current(version))return;
   dialog.close();notice(`Imported ${out.count} paused feed${out.count===1?'':'s'}. Resume a feed to start fetching its items.`,true);
   await load();
  }catch(e){if(current(version))failure(e.message);else if(csrf)notice(e.message);}
  finally{
   importing=false;fileInput.disabled=false;closeButton.disabled=false;
   if(current(version))confirmButton.disabled=!candidate;
  }
 };
})();
