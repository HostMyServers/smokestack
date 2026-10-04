/* smokestack back-office — languages.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* --------------------------------------------------------- languages */
async function viewLangs(m){
  H.render(m,html`<h2>Languages</h2>
    <p class="lead">Public pages are translated by JSON language files. English is the mandatory
      reference: without <code>en.json</code> the instance refuses to start, and any key missing
      from another language is shown in English. To add or fix a language without rebuilding,
      drop <code>xx.json</code> into <code>/var/lib/smokestack/i18n/</code> and reload.</p>
    <div id="l">Loading…</div>`);
  try{
    const meta=await api("GET","/api/v1/i18n");
    H.render($("#l"),html`<div class="card"><h3>Loaded files
      <span style="margin-left:auto"><button class="btn s" id="rl">Reload</button></span></h3><div class="body">
      <table><thead><tr><th>Code</th><th>Language</th><th>Coverage</th><th>Source</th><th>Warnings</th><th></th></tr></thead><tbody>${meta.languages.map(l=>html`<tr><td class="mono">${l.code}${l.code===meta.base?html` <span class="badge b-n">reference</span>`:""}
        ${l.code===meta.default?html` <span class="badge b-ok">default</span>`:""}</td>
        <td>${l.name}</td>
        <td><div style="display:flex;align-items:center;gap:8px"><div class="bar"><i style="width:${Math.round(l.coverage*100)}%;
          ${l.coverage<1?"background:var(--warn)":""}"></i></div><span style="font-size:12px">${Math.round(l.coverage*100)} %</span></div></td>
        <td style="font-size:12px">${l.source}</td>
        <td style="font-size:12px">${(l.warnings||[]).length?html`<span class="badge b-warn">${l.warnings.length}</span>`:"—"}</td>
        <td style="text-align:right">${l.missing?html`<button class="btn s" data-miss="${l.code}">${l.missing} missing</button>`:""}</td></tr>`)}</tbody></table><div id="miss"></div></div></div>`);
    $("#rl").onclick=async()=>{try{await api("POST","/api/v1/admin/i18n/reload");toast("Language files reloaded");render();}
      catch(e){toast(e.message,true);}};
    document.querySelectorAll("[data-miss]").forEach(b=>b.onclick=async()=>{
      const r=await api("GET","/api/v1/admin/i18n/"+b.dataset.miss+"/missing");
      H.render($("#miss"),html`<div class="note" style="margin-top:12px"><strong>${r.code}</strong>: keys to translate
        <div class="mono" style="font-size:12px;margin-top:6px">${r.missing.flatMap((x,i)=>i?[html`<br>`,x]:[x])}</div></div>`);
    });
  }catch(e){H.render($("#l"),html`<div class="note">${e.message}</div>`);}
}
