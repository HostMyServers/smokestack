/* smokestack back-office — helpers, shared state and the API client.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
const $=s=>document.querySelector(s);
let CSRF="", ME=null, VIEW="dash", PENDING=0, UNREAD=0, LANGS=[], TR_TARGET=null;

function toast(msg,err){
  const t=$("#toast");t.textContent=msg;t.className="toast"+(err?" err":"");
  t.style.display="block";clearTimeout(t._t);
  t._t=setTimeout(()=>t.style.display="none",err?5200:2600);
}
async function api(method,path,body){
  const h={};
  if(body!==undefined){h["Content-Type"]="application/json";}
  if(CSRF) h["X-CSRF-Token"]=CSRF;
  // Rien de ce que lit le back-office ne doit sortir d'un cache. Il lit
  // des endpoints publics, /api/v1/site en tête, qui s'annoncent
  // cachables parce qu'ils le sont pour un visiteur : l'exploitant qui
  // vient d'enregistrer, lui, doit voir ce qu'il a enregistré.
  const r=await fetch(path,{method,headers:h,cache:"no-store",
    body:body!==undefined?JSON.stringify(body):undefined,credentials:"same-origin"});
  if(r.status===204) return null;
  let data=null;
  try{ data=await r.json(); }catch(e){}
  if(!r.ok) throw new Error((data&&data.error)||("HTTP "+r.status));
  return data;
}
// Le balisage est assemblé par le gabarit de html.js : il échappe par
// défaut, et H.raw serait le seul moyen de dire qu'une valeur est déjà
// du balisage — le back-office n'en a besoin nulle part.
const { html } = H;
// An empty numeric field means "inherit", which the API spells as zero.
const num0=v=>{const t=String(v==null?"":v).trim();return t===""?0:parseInt(t,10)||0;};
// What this target would inherit if its fields stay empty, shown under them.
async function showInherited(catID){
  const box=$("#tinh"); if(!box) return;
  if(!catID){box.style.display="none";return;}
  try{
    const r=await api("GET","/api/v1/admin/categories/"+catID+"/params");
    const p=r.params||{}, shipped={interval_s:60,packets:20,spacing_ms:500,timeout_ms:2000};
    const eff=k=>p[k]||shipped[k];
    box.textContent=("Left empty, this target measures "+eff("packets")+" packets spaced by "+
      eff("spacing_ms")+" ms every "+eff("interval_s")+" s, timing out after "+eff("timeout_ms")+
      " ms"+(Object.keys(p).length?" — from this category":" — the shipped values, this category lending none")+
      ". Changing the category later changes this target with it.");
    box.style.display="";
    ["interval_s","packets","spacing_ms","timeout_ms"].forEach((k,i)=>{
      const el=$("#"+["tiv","tpk","tsp","tto"][i]); if(el) el.placeholder=String(eff(k));
    });
  }catch(e){box.style.display="none";}
}
// Échapper ne protège pas un href : javascript:… reste cliquable. Toute
// URL qui vient d'ailleurs (pair, PeeringDB) passe par ici, et revient
// non échappée parce que le gabarit où elle atterrit s'en charge.
const escURL = H.url;

const dt=ts=>ts?new Date(ts*1000).toLocaleString("en-GB",
  {day:"2-digit",month:"short",year:"2-digit",hour:"2-digit",minute:"2-digit"}):"—";
const gb=b=>(b/1073741824).toFixed(2)+" GB";
const ago=s=>s<60?s+" s ago":s<3600?Math.round(s/60)+" min ago":Math.round(s/3600)+" h ago";
