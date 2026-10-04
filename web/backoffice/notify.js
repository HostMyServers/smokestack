/* smokestack back-office — NOC alerting.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ------------------------------------------------------ NOC alerting */
async function viewNotify(m){
  m.innerHTML=`<h2>NOC alerting</h2>
    <p class="lead">A notification is sent only when four conditions are met: at least three
      independent observers confirm the problem, the five-minute grace period has passed, the
      AS concerned has not acknowledged, and it is an approved peer, that is an operator who
      agreed to it by joining. No unsolicited email.</p>
    <div id="n">Loading…</div>`;
  try{
    const id=await api("GET","/api/v1/admin/fed/identity");
    const c=id.notify||{};
    $("#n").innerHTML=`<div class="card"><h3>Transport</h3><div class="body">
      <label class="chk"><input type="checkbox" id="en" ${c.enabled?"checked":""}><span>Notifications enabled</span></label>
      <div class="row2">
        <div class="field"><label>SMTP server</label><input id="sh" value="${esc(c.smtp_host||"")}"></div>
        <div class="field"><label>Port</label><input id="sp" type="number" value="${c.smtp_port||587}"></div>
        <div class="field"><label>User</label><input id="su" value="${esc(c.smtp_user||"")}"></div>
        <div class="field"><label>Password</label><input id="sw" type="password" placeholder="unchanged"></div>
        <div class="field"><label>Sender</label><input id="sf" value="${esc(c.from||"")}"></div>
        <div class="field"><label>Webhook (optional)</label><input id="wh" value="${esc(c.webhook_url||"")}"></div>
      </div>
      <button class="btn p" id="save">Save</button>
    </div></div>
    <div class="card"><h3>Federated incidents</h3><div class="body" id="inc">Loading…</div></div>`;
    document.querySelectorAll("[data-goto]").forEach(a=>a.onclick=e=>{e.preventDefault();go(a.dataset.goto);});
    $("#save").onclick=async()=>{
      try{
        await api("PUT","/api/v1/admin/fed/notify",{
          enabled:$("#en").checked,smtp_host:$("#sh").value,
          smtp_port:parseInt($("#sp").value,10),smtp_user:$("#su").value,
          smtp_pass:$("#sw").value,from:$("#sf").value,webhook_url:$("#wh").value});
        toast("Saved");render();
      }catch(e){toast(e.message,true);}
    };
    const incs=await api("GET","/api/v1/fed/incidents").catch(()=>[]);
    $("#inc").innerHTML=(incs&&incs.length)?`<table><thead><tr><th>AS concerned</th>
      <th>Target</th><th>Finding</th><th>Confirmed by</th><th>State</th><th>Opened</th>
      </tr></thead><tbody>`+incs.map(i=>`<tr><td class="mono">${esc(i.suspect_asn)}</td>
      <td class="mono">${esc(i.target)}</td><td style="font-size:12px">${esc(i.detail)}</td>
      <td>${i.corroborated}</td>
      <td><span class="badge ${i.ack_at?"b-ok":i.notified_at?"b-crit":"b-warn"}">
        ${i.ack_at?"acknowledged":i.notified_at?"NOC notified":"grace period"}</span></td>
      <td style="font-size:12px">${dt(i.opened_at)}</td></tr>`).join("")+`</tbody></table>`
      :`<div class="empty">No incident.</div>`;
  }catch(e){$("#n").innerHTML=`<div class="note">${esc(e.message)}</div>`;}
}
