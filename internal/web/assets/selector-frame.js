'use strict';
(() => {
 const parentOrigin=new URL(document.URL).origin, page=document.querySelector('#page');
 const fields=['items','title','link','content','image','date'], limit=1000, cloneLimit=120000;
 let token='', nodes=[], mirror, originals=[], views=[], indices=new WeakMap(), clones=new WeakMap(), clonedNodes=0;
 let selected=null, clicked=null, item=null, itemSelector='', field='items', recipe={type:'css',items:''}, matchedItems=[];
 const safeTags=new Set('article section main nav aside header footer div span p h1 h2 h3 h4 h5 h6 ul ol li dl dt dd blockquote pre code strong em b i small time figure figcaption table thead tbody tfoot tr td th br hr'.split(' '));
 const setTheme=theme=>{document.documentElement.dataset.theme=theme==='dark'?'dark':'light';};
 const simple=/^[a-zA-Z_][a-zA-Z0-9_-]*$/, heading='h1,h2,h3,h4,h5,h6';
 const send=data=>parent.postMessage({...data,token},parentOrigin);
 const index=el=>indices.get(el), meta=el=>nodes[index(el)]||{}, view=el=>views[index(el)];
 const links=el=>[...(el.localName==='a'?[el]:[]),...el.querySelectorAll('a')].filter(a=>meta(a).link);
 const query=(root,s)=>{try{return s==='.'?[root]:[...root.querySelectorAll(s)];}catch{return [];}};
 function build(data){
  nodes=data;originals=[];views=[];indices=new WeakMap();clones=new WeakMap();clonedNodes=0;
  // An XML document is inert even when selectors need source href/src attributes.
  // Source metadata is never attached to an HTML element in the visible page.
  mirror=document.implementation.createDocument(null,'',null);page.replaceChildren();selected=clicked=item=null;itemSelector='';matchedItems=[];field='items';
  for(let i=0;i<nodes.length;i++){
   const n=nodes[i], op=n.parent<0?mirror:originals[n.parent], vp=n.parent<0?page:views[n.parent];
   if(!op)continue;
   if(!n.tag){
    if(op.nodeType!==Node.DOCUMENT_NODE){const text=mirror.createTextNode(n.text||'');op.append(text);originals[i]=text;indices.set(text,i);}
    if(vp)vp.append(document.createTextNode(n.text||''));continue;
   }
   const o=mirror.createElement(n.tag), attrs=n.attrs||{id:n.id,class:n.class,'data-testid':n.testid};
   for(const [key,value] of Object.entries(attrs))if(typeof value==='string'&&(n.attrs||value))o.setAttribute(key,value);
   op.append(o);originals[i]=o;indices.set(o,i);
   if(n.hidden||!vp)continue;
   const v=document.createElement(safeTags.has(n.tag)?n.tag:'span');v.dataset.node=i;views[i]=v;
   if(n.tag==='html'||n.tag==='body')v.classList.add('page-root');
   if(n.tag==='a')v.classList.add('source-link');
   if(n.tag==='img'){v.classList.add('image-placeholder');v.textContent='Image'+(n.alt?': '+n.alt:'');}
   v.tabIndex=0;vp.append(v);
  }
  for(const o of originals){
   if(!o||o.localName!=='a'||!view(o))continue;
   const v=view(o);
   if(v.querySelector('div,article,section,h1,h2,h3,img,.image-placeholder'))v.classList.add('linked-card');
   if(meta(o).link&&!v.textContent.trim()){v.textContent='Article link';v.classList.add('empty-link');}
  }
  send({type:'loaded'});
 }
 function xpathNodes(root,selector,first=false){
  const doc=root.nodeType===Node.DOCUMENT_NODE?root:root.ownerDocument;
  // Requesting a node result rejects scalar expressions such as count(//article).
  const result=doc.evaluate(selector,root,null,first?XPathResult.FIRST_ORDERED_NODE_TYPE:XPathResult.ORDERED_NODE_ITERATOR_TYPE,null);
  if(first)return result.singleNodeValue?[result.singleNodeValue]:[];
  const out=[];let n;while((n=result.iterateNext())){out.push(n);if(out.length>limit)throw Error('Selector matches more than 1,000 items. Narrow the repeated-item selector.');}return out;
 }
 function selectNodes(root,type,selector,first=false){
  if(!selector)return [];
  if(typeof selector!=='string'||selector.length>1000)throw Error('Selectors must be at most 1,000 characters.');
  if(type==='xpath')return xpathNodes(root,selector,first);
  if(selector==='.')return [root];
  if(first){const result=root.querySelector(selector);return result?[result]:[];}
  const out=[...root.querySelectorAll(selector)];if(out.length>limit)throw Error('Selector matches more than 1,000 items. Narrow the repeated-item selector.');return out;
 }
 function isolated(card){
  if(clones.has(card))return clones.get(card);
  const walker=mirror.createTreeWalker(card,NodeFilter.SHOW_ALL);let count=1;while(walker.nextNode())count++;
  if(clonedNodes+count>cloneLimit)throw Error('Repeated items overlap too broadly. Select smaller, separate cards.');
  const doc=document.implementation.createDocument(null,'',null), root=doc.importNode(card,true);doc.append(root);clonedNodes+=count;
  const pairs=[[card,root]];while(pairs.length){const [original,copy]=pairs.pop();indices.set(copy,index(original));for(let a=original.firstChild,b=copy.firstChild;a&&b;a=a.nextSibling,b=b.nextSibling)pairs.push([a,b]);}
  clones.set(card,root);return root;
 }
 function fieldNodes(card,type,selector){
  if(!selector)return [];
  if(type==='xpath'&&!selector.startsWith('.'))throw Error('XPath fields must be relative and start with . (for example .//a/@href).');
  return selectNodes(type==='xpath'?isolated(card):card,type,selector,true).filter(n=>n.nodeType!==Node.DOCUMENT_NODE);
 }
 function owningElement(n){if(!n)return null;if(n.nodeType===Node.ATTRIBUTE_NODE)n=n.ownerElement;if(n.nodeType!==Node.ELEMENT_NODE)n=n.parentElement;return n;}
 function clearHighlights(){for(const v of views)if(v)v.classList.remove('matched','field-matched');}
 function errorText(error){
  if(error.name==='SyntaxError'||error.name==='InvalidExpressionError')return 'Invalid '+(recipe.type==='xpath'?'XPath':'CSS')+' selector. Check its syntax.';
  if(error.name==='TypeError'||error.name==='XPathException')return 'XPath selectors must return elements, attributes, or text nodes, rather than a scalar value.';
  return String(error.message||'Could not evaluate this selector.').slice(0,240);
 }
 function matchRecipe(revision){
  clearHighlights();matchedItems=[];item=null;let count=0,visible=0,items=0,error='';
  try{
   if(!['css','xpath'].includes(recipe.type))throw Error('Choose CSS or XPath selectors.');
   matchedItems=selectNodes(mirror,recipe.type,recipe.items);items=matchedItems.length;
   if(matchedItems.some(n=>n.nodeType!==Node.ELEMENT_NODE))throw Error('Repeated-item selectors must match elements.');
   const highlights=[];
   if(field==='items'){count=items;for(const n of matchedItems){const v=view(n);if(v)highlights.push(v);}}
   else for(const card of matchedItems){const matches=fieldNodes(card,recipe.type,recipe[field]?.selector||'');if(matches.length){count++;const v=view(owningElement(matches[0]));if(v)highlights.push(v);}}
   for(const card of matchedItems)view(card)?.classList.add('matched');
   if(field!=='items')for(const v of highlights)v.classList.add('field-matched');
   visible=highlights.length;itemSelector=recipe.items;
   if(selected)item=matchedItems.find(card=>card.contains(selected))||null;
  }catch(e){error=errorText(e);matchedItems=[];count=visible=0;clearHighlights();}
  send({type:'matches',revision,items:Math.min(items,limit),count,visible,error,field});
 }
 function literal(value){
  if(!value.includes("'"))return "'"+value+"'";
  if(!value.includes('"'))return '"'+value+'"';
  return 'concat('+value.split("'").map(part=>"'"+part+"'").join(',"\'",')+')';
 }
 // Convert the subset generated by this picker. Reject unknown syntax instead
 // of silently producing an XPath expression with different matching behavior.
 function splitGroups(selector){
  const groups=[];let start=0,depth=0,quote='';
  for(let i=0;i<selector.length;i++){const c=selector[i];if(c==='\\')throw Error('CSS escapes need manual XPath conversion.');if(quote){if(c===quote)quote='';continue;}if(c==='"'||c==="'"){quote=c;continue;}if(c==='('||c==='[')depth++;if(c===')'||c===']')depth--;if(depth<0)throw Error('Unbalanced CSS selector.');if(c===','&&depth===0){groups.push(selector.slice(start,i));start=i+1;}}
  if(depth||quote)throw Error('Unbalanced CSS selector.');groups.push(selector.slice(start));return groups;
 }
 function cssXPath(selector,relative=false,depth=0){
  if(!selector||selector==='.')return selector;
  if(depth>12)throw Error('CSS selector is too deeply nested to convert.');
  return splitGroups(selector).map(group=>{
   let rest=group.trim(), path='', relation=relative?'.//':'//';
   if(rest.startsWith('>')){if(!relative)throw Error('A child selector needs a parent.');relation='./';rest=rest.slice(1).trim();}
   while(rest){
    let end=0,nesting=0,quote='';for(;end<rest.length;end++){const c=rest[end];if(quote){if(c===quote)quote='';continue;}if(c==='"'||c==="'"){quote=c;continue;}if(c==='('||c==='[')nesting++;if(c===')'||c===']')nesting--;if(!nesting&&(/[\s>+~]/.test(c)))break;}
    if(!end)throw Error('This CSS combinator needs manual XPath conversion.');
    const compound=rest.slice(0,end);rest=rest.slice(end).trim();path+=relation+compoundXPath(compound,depth);
    relation='//';if(rest.startsWith('>')){relation='/';rest=rest.slice(1).trim();if(!rest)throw Error('Incomplete CSS child selector.');}
   }
   if(!path)throw Error('Empty CSS selector.');return path;
  }).join(' | ');
 }
 function compoundXPath(source,depth){
  const match=source.match(/^(\*|[a-zA-Z_][a-zA-Z0-9_-]*)/), tag=match?match[0]:'*';
  let rest=source.slice(match?match[0].length:0), predicates=[],position=[];
  while(rest){
   let m;
   if((m=rest.match(/^([.#])([a-zA-Z_][a-zA-Z0-9_-]*)/))){predicates.push(m[1]==='#'?'@id='+literal(m[2]):"contains(concat(' ', normalize-space(@class), ' '), "+literal(' '+m[2]+' ')+')');rest=rest.slice(m[0].length);continue;}
   if((m=rest.match(/^\[([a-zA-Z_][a-zA-Z0-9_-]*)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([a-zA-Z0-9_-]+)))?\]/))){predicates.push('@'+m[1]+(m[2]!==undefined||m[3]!==undefined||m[4]!==undefined?'='+literal(m[2]??m[3]??m[4]):''));rest=rest.slice(m[0].length);continue;}
   if((m=rest.match(/^:nth-of-type\(([1-9][0-9]*)\)/))){if(tag==='*')throw Error('Wildcard :nth-of-type needs manual XPath conversion.');position.push(m[1]);rest=rest.slice(m[0].length);continue;}
   if(rest.startsWith(':has(')){
    let i=5,level=1,quote='';for(;i<rest.length;i++){const c=rest[i];if(quote){if(c===quote)quote='';continue;}if(c==='"'||c==="'"){quote=c;continue;}if(c==='(')level++;if(c===')'&&!--level)break;}
    if(level)throw Error('Unbalanced CSS :has selector.');predicates.push(cssXPath(rest.slice(5,i),true,depth+1));rest=rest.slice(i+1);continue;
   }
   throw Error('This CSS selector needs manual XPath conversion: '+rest.slice(0,60));
  }
  return tag+[...position,...predicates].map(p=>'['+p+']').join('');
 }
 function convertRecipe(source){
  if(source.type!=='css')throw Error('Only CSS recipes can be converted to XPath.');
  const result=JSON.parse(JSON.stringify(source));result.type='xpath';result.items=cssXPath(source.items||'');
  for(const name of fields.slice(1))if(source[name])result[name]={...source[name],selector:cssXPath(source[name].selector||'',true)};
  for(const selector of [result.items,...fields.slice(1).map(name=>result[name]?.selector||'')]){if(selector.length>1000)throw Error('Converted XPath exceeds the 1,000-character selector limit.');if(selector)mirror.createExpression(selector,null);}
  return result;
 }
 function segment(el){return el.localName+[...el.classList].filter(c=>simple.test(c)).slice(0,3).map(c=>'.'+c).join('');}
 function variants(el){
  const tag=el.localName,classes=[...el.classList].filter(c=>simple.test(c)).slice(0,8),opts=[];
  if(meta(el).testid&&simple.test(meta(el).testid))opts.push(tag+'[data-testid="'+meta(el).testid+'"]');
  opts.push(segment(el),...classes.map(c=>tag+'.'+c),tag);return [...new Set(opts)];
 }
 function exact(el,root){
  const parts=[];for(let n=el;n&&n!==root;n=n.parentElement){const same=[...(n.parentNode?.children||[])].filter(s=>s.localName===n.localName);parts.unshift(n.localName+':nth-of-type('+(same.indexOf(n)+1)+')');}return parts.join(' > ');
 }
 function disjoint(matches){const set=new Set(matches);return !matches.some(el=>{for(let p=el.parentElement;p;p=p.parentElement)if(set.has(p))return true;return false;});}
 function itemChoices(el){
  const opts=variants(el),h=el.querySelector(heading),marker=h||el.querySelector('time,.date');
  if(marker)opts.unshift(...variants(el).map(s=>s+':has('+(h?h.localName:variants(marker)[0])+')'));
  for(let p=el.parentElement,depth=0;p&&depth<3;p=p.parentElement,depth++){if(p.id&&simple.test(p.id))opts.push('#'+p.id+' '+variants(el)[0]);if(p.classList.length)opts.push(variants(p)[0]+' > '+variants(el)[0]);}
  opts.push(exact(el,null));
  return [...new Set(opts)].map(css=>{try{const selector=recipe.type==='xpath'?cssXPath(css):css;if(selector.length>1000)return null;const matches=selectNodes(mirror,recipe.type,selector);return {selector,count:matches.length,disjoint:disjoint(matches)};}catch{return null;}}).filter(c=>c&&c.count>0&&c.count<=limit&&c.disjoint).sort((a,b)=>(b.count>1)-(a.count>1));
 }
 function choices(el){
  if(field==='items')return itemChoices(el);
  if(!item||!item.contains(el))return [];
  const cards=matchedItems.length?matchedItems:query(mirror,itemSelector);
  if(el===item)return [{selector:'.',count:cards.length}];
  const opts=[...variants(el),exact(el,item)];
  return [...new Set(opts)].map(css=>{try{
   const selector=recipe.type==='xpath'?cssXPath(css,true):css;if(selector.length>1000)return null;
   const own=fieldNodes(item,recipe.type,selector);if(!own.length||index(owningElement(own[0]))!==index(el))return null;
   // A suggestion should identify this particular field within the chosen card.
   if(query(item,css).length!==1)return null;
   return {selector,count:cards.filter(card=>fieldNodes(card,recipe.type,selector).length).length};
  }catch{return null;}}).filter(Boolean).sort((a,b)=>b.count-a.count||a.selector.length-b.selector.length);
 }
 function cardFor(el){
  let best=el,score=-Infinity;
  for(let p=el,depth=0;p&&p.localName!=='body'&&depth<12;p=p.parentElement,depth++){
   if(!view(p))continue;const hs=p.querySelectorAll(heading).length,ls=links(p).length;if(hs>1||ls===0||ls>12)continue;
   const c=itemChoices(p).find(c=>c.count>1);if(!c)continue;
   const semantic={article:12,li:10,tr:10,p:4,a:1,div:2,span:0}[p.localName]??0,points=semantic+(hs===1?4:0)+(p.querySelector('img')?3:0)-depth*.3;
   if(points>score){best=p;score=points;}
  }return best;
 }
 function fieldTarget(el){
  if(field==='title'){const h=el.closest(heading);if(h&&item?.contains(h))return h;}
  if(field==='date'){const t=el.closest('time');if(t&&item?.contains(t))return t;}
  if(field==='link'&&item?.contains(el)){
   const a=el.closest('a');if(a&&item.contains(a)&&meta(a).link)return a;if(item.localName==='a'&&meta(item).link)return item;
   const candidates=links(item),title=(view(el)?.textContent||'').trim();return candidates.find(a=>(view(a)?.textContent||'').trim()===title)||candidates.find(a=>view(a)?.classList.contains('empty-link'))||candidates[0]||el;
  }return el;
 }
 function ancestors(){const list=[];for(let p=clicked,depth=0;p&&p.localName!=='body'&&depth<12;p=p.parentElement,depth++){if(!view(p))continue;const c=itemChoices(p)[0];list.push({node:index(p),label:(p.localName+(p===selected?' (selected)':'')+' · '+(c?.count||0)+' matches'+(p.querySelector(heading)?' · contains headline':'')).slice(0,200),selected:p===selected});}return list;}
 function select(el){
  if(!el||!view(el))return;
  if(field!=='items')item=matchedItems.find(card=>card.contains(el))||item;
  el=fieldTarget(el);selected=el;
  for(const v of views)if(v)v.classList.remove('selected');view(el)?.classList.add('selected');
  const n=meta(el),opts=choices(el).slice(0,14);if(field==='items'&&opts.length)highlight(opts[0].selector);
  send({type:'selection',field,label:segment(el).slice(0,200),choices:opts,attr:field==='link'&&n.link?'href':field==='date'&&n.datetime?'datetime':'',hasParent:!!el.parentElement&&!!view(el.parentElement),ancestors:field==='items'?ancestors():[]});
 }
 function highlight(selector){clearHighlights();try{for(const el of selectNodes(mirror,recipe.type,selector))view(el)?.classList.add('matched');}catch{/* A stale choice cannot leave stale highlights. */}}
 function pick(v){clicked=originals[Number(v.dataset.node)];select(field==='items'?cardFor(clicked):clicked);}
 page.addEventListener('click',e=>{e.preventDefault();e.stopPropagation();const v=e.target.closest('[data-node]');if(v)pick(v);});
 page.addEventListener('keydown',e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();const v=e.target.closest('[data-node]');if(v)pick(v);}});
 window.addEventListener('message',e=>{
  if(e.source!==parent||e.origin!==parentOrigin||!e.data||typeof e.data!=='object')return;const d=e.data;
  if(d.type==='init'&&!token&&typeof d.token==='string'&&/^[a-f0-9]{32}$/.test(d.token)&&Array.isArray(d.nodes)&&d.nodes.length<=12000){token=d.token;setTheme(d.theme);build(d.nodes);return;}
  if(!token||d.token!==token)return;
  if(d.type==='theme')setTheme(d.theme);
  if(d.type==='recipe'&&d.recipe&&typeof d.recipe==='object'&&Number.isInteger(d.revision)){
   if(recipe.type!==d.recipe.type||recipe.items!==d.recipe.items){clones=new WeakMap();clonedNodes=0;}
   if(recipe.type!==d.recipe.type)selected=null;
   for(const v of views)if(v)v.classList.remove('selected');
   recipe=d.recipe;if(fields.includes(d.field))field=d.field;matchRecipe(d.revision);
  }
  if(d.type==='convert'&&d.recipe&&typeof d.recipe==='object'&&Number.isInteger(d.revision)){
   try{send({type:'converted',revision:d.revision,recipe:convertRecipe(d.recipe),error:''});}catch(e){send({type:'converted',revision:d.revision,recipe:d.recipe,error:String(e.message||'Could not convert selectors.').slice(0,240)});}
  }
  if(d.type==='field'&&fields.includes(d.field)){field=d.field;if(selected)select(selected);}
  if(d.type==='parent'&&selected)select(selected.parentElement);
  if(d.type==='ancestor'&&field==='items'&&Number.isInteger(d.node)&&ancestors().some(a=>a.node===d.node))select(originals[d.node]);
  if(d.type==='highlight'&&field==='items'&&selected&&choices(selected).some(c=>c.selector===d.selector))highlight(d.selector);
  if(d.type==='commit'&&selected&&typeof d.selector==='string'&&choices(selected).some(c=>c.selector===d.selector)){
   if(field==='items'){if(recipe.items!==d.selector){clones=new WeakMap();clonedNodes=0;}item=selected;itemSelector=d.selector;recipe={...recipe,items:itemSelector};try{matchedItems=selectNodes(mirror,recipe.type,itemSelector);}catch{matchedItems=[];}highlight(itemSelector);}
   send({type:'committed',field,selector:d.selector,selectorType:recipe.type});
  }
 });
 parent.postMessage({type:'ready'},parentOrigin);
})();
