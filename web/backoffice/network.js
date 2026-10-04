/* smokestack back-office — host network.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ------------------------------------------------------ host network */
async function viewNet(m){
  m.innerHTML=`<h2>Host network</h2>
    <p class="lead">Public information about the AS hosting this instance, from RIPEstat
      (observed routing) and PeeringDB (declared by the operator). Cached for 24 h and shown on
      the public page <a href="/network" target="_blank">/network</a>. The AS number is set in the
      Publisher page.</p>
    <div id="n">Loading…</div>`;
  const show=i=>{
    const p=i.peeringdb;
    $("#n").innerHTML=`<div class="card"><h3>AS${esc(i.asn)} — ${esc(i.holder||"?")}
      <span style="margin-left:auto"><button class="btn s" id="rf">Refresh</button></span></h3><div class="body">
      <table><tbody>
        <tr><td style="width:220px">Country</td><td>${esc(i.country||"—")}</td></tr>
        <tr><td>Announced prefixes</td><td>${i.prefixes_v4} IPv4 · ${i.prefixes_v6} IPv6</td></tr>
        <tr><td>Upstream / downstream neighbours</td><td>${i.nb_upstreams} / ${i.nb_downstreams}</td></tr>
        <tr><td>Main upstream neighbours</td><td>${(i.upstreams||[]).map(u=>`AS${u.asn} ${esc(u.name||"")}`).join("<br>")||"—"}</td></tr>
        <tr><td>PeeringDB record</td><td>${p?`${escURL(p.url)?`<a href="${escURL(p.url)}" target="_blank" rel="noopener noreferrer">${esc(p.name)}</a>`:esc(p.name)} · ${esc(p.policy||"")}`:"none"}</td></tr>
        <tr><td>Internet exchanges</td><td>${p?(p.ixs||[]).map(x=>esc(x.name)).join(", ")||"—":"—"}</td></tr>
        <tr><td>Facilities</td><td>${p?(p.facilities||[]).map(f=>esc(f.name)).join(", ")||"—":"—"}</td></tr>
        <tr><td>Fetched</td><td>${dt(i.fetched_at)}</td></tr>
      </tbody></table>
      ${(i.errors&&i.errors.length)?`<div class="note" style="margin-top:12px;color:var(--crit)">${i.errors.map(esc).join("<br>")}</div>`:""}
    </div></div>`;
    $("#rf").onclick=async()=>{try{show(await api("POST","/api/v1/admin/asn/refresh"));toast("Refreshed");}
      catch(e){toast(e.message,true);}};
  };
  try{show(await api("GET","/api/v1/asn"));}
  catch(e){$("#n").innerHTML=`<div class="note">${esc(e.message)}</div>`;}
}
