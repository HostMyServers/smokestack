/* smokestack back-office — TLS certificates.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* --------------------------------------------------- TLS certificates */
// A certificate that expires without anyone noticing is one of the few
// outages that is entirely predictable, and therefore one of the few worth
// warning about weeks in advance rather than at the moment it breaks.
async function viewCerts(m){
  H.render(m,html`<h2>TLS certificates</h2>
    <p class="lead">Every TCP target with a port is inspected twice a day: one TLS handshake, the
      certificate read, and nothing kept in the latency series — a handshake is far slower than a
      connection and would distort the measurement. You are warned once per threshold crossed,
      not once per check, so a certificate left alone for a month produces a handful of messages
      rather than sixty. A renewal rearms the whole sequence.</p>
    <div class="card"><div class="body" id="ce">Loading…</div></div>
    <div class="card"><h3>Certificates seen<span style="margin-left:auto">
      <button class="btn s" id="ce_now">Inspect now</button></span></h3>
      <div class="body" id="cetab">Loading…</div></div>`);
  const badge=(r)=>{
    if(r.off) return html`<span class="badge b-n">not watched</span>`;
    if(!r.cert) return html`<span class="badge b-n">not inspected yet</span>`;
    if(r.cert.problem) return html`<span class="badge b-crit">refused</span>`;
    const d=r.cert.days_left;
    if(d<0) return html`<span class="badge b-crit">expired</span>`;
    if(d<=7) return html`<span class="badge b-crit">${d} d left</span>`;
    if(d<=30) return html`<span class="badge b-warn">${d} d left</span>`;
    return html`<span class="badge b-ok">${d} d left</span>`;
  };
  const table=(rows,stages)=>{
    if(!rows||!rows.length) return html`<div class="empty">No TCP target with a port yet. A certificate is
      only inspected on a target measured over TCP, since that is where a port and a handshake exist.</div>`;
    return html`<table><thead><tr><th>Target</th><th>State</th><th>Expires</th><th>Issuer</th>
      <th>Names</th><th></th></tr></thead><tbody>${rows.map(r=>{
      const c=r.cert;
      return html`<tr><td>${r.title}<div class="mono" style="font-size:11.5px;color:var(--ink2)">${r.host}:${r.port}${r.family?" · IPv"+r.family:""}</div></td>
        <td>${badge(r)}</td>
        <td>${c&&c.not_after?new Date(c.not_after*1000).toLocaleDateString():"—"}</td>
        <td>${c?c.issuer||"—":"—"}</td>
        <td class="mono" style="font-size:11.5px">${c&&c.dns_names&&c.dns_names.length?c.dns_names.slice(0,3).join(", ")+(c.dns_names.length>3?" +"+(c.dns_names.length-3):""):"—"}</td>
        <td><button class="btn s" data-off="${r.target_id}" data-now="${r.off?1:0}">${r.off?"Watch":"Stop watching"}</button></td></tr>${
        c&&c.problem?html`<tr><td colspan="6" style="background:var(--bg2);color:var(--crit);font-size:12.5px">${c.problem}</td></tr>`:""}`;
    })}</tbody></table>
    <div class="note">Alert thresholds: ${stages.join(", ")} days before expiry, plus one the day it
      expires and one when the certificate is refused outright — a wrong name, an unverifiable chain,
      or a handshake that does not complete. The first threshold is the one you set above; the others
      follow it.</div>`;
  };
  const wire=()=>{
    document.querySelectorAll("[data-off]").forEach(b=>b.onclick=async()=>{
      try{
        await api("PATCH","/api/v1/admin/targets/"+b.dataset.off,{cert_off:b.dataset.now!=="1"});
        toast("Saved"); viewCerts(m);
      }catch(e){toast(e.message,true);}
    });
  };
  try{
    const d=await api("GET","/api/v1/admin/certs"); const c=d.config||{};
    H.render($("#ce"),html`
      <label class="chk"><input type="checkbox" id="ce_en" ${c.enabled!==false?"checked":""}>
        <span>Warn me before a certificate expires<small>On by default. Turning it off stops the
          messages but keeps the inspection, so the table below stays current.</small></span></label>
      <div class="row2">
        <div class="field"><label>First warning, in days before expiry</label>
          <input id="ce_days" type="number" min="1" max="365" value="${c.warn_days||30}">
          <small>Thirty days suits an automated renewal that may fail silently; sixty suits a
            certificate someone still orders by hand.</small></div>
        <div class="field"><label>Extra recipients (never public)</label>
          <input id="ce_to" value="${c.recipients||""}" placeholder="web@example.net, pki@example.net">
          <small>In addition to the notification channels. Useful when the certificate belongs to a
            team that is not the one receiving network incidents.</small></div>
      </div>
      <div class="note">Messages leave through the channels configured in
        <a href="#" data-goto="channels">Notification channels</a>, plus the addresses above.</div>
      <button class="btn p" id="ce_save">Save</button>`);
    document.querySelectorAll("[data-goto]").forEach(a=>a.onclick=e=>{e.preventDefault();go(a.dataset.goto);});
    $("#ce_save").onclick=async()=>{
      try{await api("PUT","/api/v1/admin/certs",{enabled:$("#ce_en").checked,
        warn_days:parseInt($("#ce_days").value,10),recipients:$("#ce_to").value});
        toast("Saved");}catch(e){toast(e.message,true);}
    };
    H.render($("#cetab"),table(d.rows,d.stages||[30,14,7,1])); wire();
    $("#ce_now").onclick=async()=>{
      const b=$("#ce_now"); b.disabled=true; b.textContent="Inspecting…";
      try{const r=await api("POST","/api/v1/admin/certs/check",{});
        H.render($("#cetab"),table(r.rows,d.stages||[30,14,7,1])); wire(); toast("Inspected");}
      catch(e){toast(e.message,true);}
      finally{b.disabled=false; b.textContent="Inspect now";}
    };
  }catch(e){H.render($("#ce"),html`<div class="note">${e.message}</div>`);}
}
