/* smokestack back-office — dashboard.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* --------------------------------------------------------- dashboard */
function probeCard(p){
  if(!p) return "";
  const age=p.last_seen_at?Math.round(Date.now()/1000-p.last_seen_at):null;
  const alive=age!=null&&age<120, W=p.writer;
  const mode={external:"isolated process",embedded:"embedded in the service",disabled:"disabled"}[p.mode]||p.mode;
  return `<div class="card"><h3>Measurement pipeline
    <span style="margin-left:auto">${p.mode==="disabled"?'<span class="badge b-n">disabled</span>'
      :alive?'<span class="badge b-ok">probe active</span>':'<span class="badge b-crit">probe silent</span>'}</span></h3><div class="body">
    <table><tbody>
      <tr><td style="width:240px">Probe mode</td><td>${esc(mode)}${p.mode==="embedded"
        ?' <span style="font-size:12px;color:var(--ink2)">— the isolated mode protects measurements better under heavy load</span>':""}</td></tr>
      <tr><td>Last measurement received</td><td>${age==null?"—":ago(age)}</td></tr>
      <tr><td>Measurements written / dropped</td><td>${W.written} / <span style="color:${W.dropped?"var(--crit)":"inherit"}">${W.dropped}</span></td></tr>
      <tr><td>Write queue</td><td>${W.queued} / ${W.capacity} · last batch ${W.last_batch_ms.toFixed(1)} ms</td></tr>
      <tr><td>Home page overview</td><td>built in ${p.overview_build_ms} ms, every 30 s</td></tr>
    </tbody></table></div></div>`;
}
// A container without the host network distorts every measurement: say so
// where the operator will see it.
async function containerBanner(){
  try{
    const c=await api("GET","/api/v1/admin/container");
    if(!c.in_container) return "";
    const bad=!c.host_network;
    return `<div class="note" style="margin:0 0 16px;border-left:3px solid ${bad?"var(--crit)":"var(--warn)"}">
      <strong>${bad?"Container without the host network":"Running in a container"}</strong><br>${esc(c.message)}.
      ${bad?`Until then, the numbers below include Docker's NAT and are not comparable with a native install.`:""}
      <a href="https://github.com/nkglfr/smokestack/blob/main/DEPLOY.md#12-container-image-tests" target="_blank" rel="noreferrer">Details</a></div>`;
  }catch(e){return "";}
}

async function viewDash(m){
  m.innerHTML=(await containerBanner())+`<h2>Dashboard</h2>
    <p class="lead">State of the instance, the probe and the storage.</p>
    <div id="d">Loading…</div>`;
  try{
    const [tree,st,peers,ps]=await Promise.all([
      api("GET","/api/v1/tree"),
      api("GET","/api/v1/admin/storage").catch(()=>null),
      api("GET","/api/v1/fed/peers").catch(()=>[]),
      api("GET","/api/v1/admin/probe/status").catch(()=>null)
    ]);
    const nT=(tree||[]).reduce((a,c)=>a+(c.targets||[]).length,0);
    const rep=st&&st.report?st.report:{};
    const stat=(k,v)=>`<div class="card"><div class="body"><div style="font-size:11px;color:var(--ink2)">${k}</div>
      <div style="font-size:22px;font-weight:600">${v}</div></div></div>`;
    $("#d").innerHTML=`
      <div class="row2">${stat("Categories",(tree||[]).length)}${stat("Targets",nT)}
        ${stat("Approved peers",(peers||[]).length)}${stat("Local archive",gb(rep.local_bytes||0))}</div>
      ${probeCard(ps)}
      <div class="card"><h3>Storage</h3><div class="body"><table><tbody>
        <tr><td style="width:240px">Mode</td><td><span class="badge b-n">${esc(rep.mode||"—")}</span></td></tr>
        <tr><td>Disk usage</td><td>${((rep.disk_used_pct||0)*100).toFixed(1)} %
          ${rep.degraded?'<span class="badge b-crit">degraded mode</span>':""}</td></tr>
        <tr><td>Local chunks</td><td>${rep.chunks||0}</td></tr>
        <tr><td>Pending uploads</td><td>${rep.pending_uploads||0}</td></tr>
        <tr><td>Projected retention</td><td>${rep.projected_days?rep.projected_days.toFixed(0)+" days at the current rate":"—"}</td></tr>
      </tbody></table></div></div>`;
  }catch(e){$("#d").innerHTML=`<div class="note">${esc(e.message)}</div>`;}
}
