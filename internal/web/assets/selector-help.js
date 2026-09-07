'use strict';
(() => {
 const dialog=document.querySelector('#visual-selector'), panel=document.querySelector('#selector-field-tip'), title=document.querySelector('#selector-tip-title'), content=document.querySelector('#selector-tip-content');
 let active=null, lastType='';
 const tips={
  'visual-type':{title:'Selector type',body:'Choose one syntax for all selectors. CSS uses tag names and classes; XPath uses paths. Convert to XPath translates supported CSS selections for you. Changing the type menu alone keeps your existing text.',css:'article.card',xpath:'//article'},
  'visual-items':{title:'Repeated items',body:'Find the container that repeats once per story, including its title, link, and other details. The remaining fields look inside each matched card.',css:'article.card',xpath:'//article',note:'Start here. Check that the blue outlines surround whole stories rather than just their headings.'},
  'visual-title-selector':{title:'Title selector',body:'Find the heading inside each card. The first match supplies the story’s title. A story needs a nonempty title to appear in your feed.',css:'h2',xpath:'.//h2',note:'Use . if the card itself contains the title text you want.'},
  'visual-title-attr':{title:'Title attribute',body:'Leave blank to use the element’s text. To read an HTML attribute instead, enter its name, such as title or aria-label. Don’t include @.'},
  'visual-link-selector':{title:'Link selector',body:'Find the article link inside each card. Use . when the repeated card itself is a link. Leave blank to create items without article links.',css:'a',xpath:'.//a',note:'When a link selector is supplied but its link is missing or unusable, that story is skipped.'},
  'visual-link-attr':{title:'Link attribute',body:'Leave blank to read href, the link’s destination. For a custom attribute, enter its name, such as data-url. Relative links are resolved against the source page.',xpathNote:'If your XPath already ends in /@href, you can leave this box blank.'},
  'visual-content-selector':{title:'Description selector',body:'Find the summary or description inside each card. The first match supplies formatted content for your reader. Leave blank for no extracted description.',css:'.summary',xpath:'.//p'},
  'visual-content-attr':{title:'Description attribute',body:'Leave blank to keep the selected element’s formatted HTML. Enter an attribute name, such as data-summary, to use its value as plain text instead.'},
  'visual-image-selector':{title:'Image selector',body:'Find an image inside each card. Leave blank to use the first image automatically. If you enter a selector and it matches nothing, that story has no selected image.',css:'img',xpath:'.//img'},
  'visual-image-attr':{title:'Image attribute',body:'Leave blank for automatic image URL detection, including common lazy-loading attributes. Enter src or data-src to use a specific attribute. This setting requires an image selector.'},
  'visual-date-selector':{title:'Date selector',body:'Find the publication date or relative label inside each card. English phrases such as “2 minutes ago”, “1 month ago”, and “yesterday” are recognized automatically. Preview items marks these dates as estimated.',css:'time',xpath:'.//time',note:'Saved stories keep their original date on later refreshes. Missing or unrecognized dates use first-seen time. Use Date settings in the feed editor for a custom format and time zone.'},
  'visual-date-attr':{title:'Date attribute',body:'Leave blank to prefer a valid exact date in the selected element’s datetime attribute, then try its displayed text. Enter an attribute name, such as title or data-date, to read only that value instead.',xpathNote:'With .//time/@datetime or .//time/text(), leave this box blank to parse exactly the selected attribute or text. Automatic datetime detection applies only when you select an element.'}
 };
 function paragraph(text){const p=document.createElement('p');p.textContent=text;content.append(p);}
 function render(){
  if(!active)return;const tip=tips[active.dataset.helpFor], type=document.querySelector('#visual-type').value;
  lastType=type;title.textContent=tip.title;content.replaceChildren();paragraph(tip.body);
  if(tip[type]){const p=document.createElement('p'), code=document.createElement('code');p.append((type==='xpath'?'XPath':'CSS')+' example: ');code.textContent=tip[type];p.append(code);content.append(p);}
  if(tip.note)paragraph(tip.note);
  if(type==='xpath'&&tip.xpathNote)paragraph(tip.xpathNote);
 }
 function close(){if(active)active.setAttribute('aria-expanded','false');active=null;panel.hidden=true;}
 for(const button of dialog.querySelectorAll('.field-help')){
  button.addEventListener('click',()=>{
   if(active===button){close();return;}close();active=button;button.setAttribute('aria-expanded','true');
   // Share a full-width disclosure so attribute help is readable even beside
   // the narrow attribute inputs, without covering the preview or typed code.
   const row=button.closest('.selector-field-row,.selector-type-row')||button.closest('.selector-control');row.append(panel);
   render();panel.hidden=false;panel.scrollIntoView({block:'nearest'});
  });
 }
 document.querySelector('#selector-tip-close').addEventListener('click',()=>{const button=active;close();button?.focus();});
 dialog.addEventListener('keydown',event=>{if(event.key==='Escape'&&active){event.preventDefault();event.stopPropagation();close();}});
 dialog.addEventListener('close',close);
 window.addEventListener('rss-selector-syntax-change',()=>{if(active&&lastType!==document.querySelector('#visual-type').value)render();});
})();
