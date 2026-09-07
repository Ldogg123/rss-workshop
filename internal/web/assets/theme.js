'use strict';
(() => {
 const preference=matchMedia('(prefers-color-scheme: dark)');
 let saved='';try{saved=localStorage.getItem('rss-theme')||'';}catch{}
 if(saved!=='light'&&saved!=='dark')saved='';
 function apply(theme){
  document.documentElement.dataset.theme=theme;
  const button=document.querySelector('#theme-toggle');
  if(button){button.textContent=theme==='dark'?'Light mode':'Dark mode';button.setAttribute('aria-label','Switch to '+(theme==='dark'?'light':'dark')+' mode');}
  window.dispatchEvent(new CustomEvent('rss-theme-change',{detail:theme}));
 }
 function current(){return saved==='light'||saved==='dark'?saved:preference.matches?'dark':'light';}
 apply(current());
 preference.addEventListener('change',()=>{if(!saved)apply(current());});
 document.addEventListener('DOMContentLoaded',()=>{
  apply(current());
  document.querySelector('#theme-toggle').onclick=()=>{
   saved=document.documentElement.dataset.theme==='dark'?'light':'dark';
   try{localStorage.setItem('rss-theme',saved);}catch{}
   apply(saved);
  };
 });
})();
