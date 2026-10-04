/* smokestack back-office — maintenance calendar.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ------------------------------------------------ maintenance calendar */
// A target you restart on purpose is not a target that is down. Left
// undeclared, it wakes the on-call for nothing and leaves a hole in the
// graph nobody can explain six months later. Two switches, because both
// needs exist: silence the alerting and keep measuring, or stop the
// measurement too so the work does not show up as a hundred per cent loss
// in the statistics and in the availability figure.
async function viewMaint(m) {
  const pad = n => String(n).padStart(2, "0");
  // A datetime-local field speaks local time and the API wants epoch: the
  // conversion belongs here rather than in the operator's head.
  const toLocal = ts => { const d = new Date(ts * 1000);
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`; };
  const fromLocal = v => v ? Math.floor(new Date(v).getTime() / 1000) : 0;
  const now = Math.floor(Date.now() / 1000);
  H.render(m, html`<h2>Maintenance calendar</h2>
    <p class="lead">Declare a window before you touch a target and the graph explains itself: the on-call is
      not woken for a restart you planned, the public page carries a banner so a reader understands the stop
      instead of opening a ticket, and — when the window stops the measurement — the passes inside it are
      left out of the availability figure rather than counted against the target as an outage.</p>
    <div class="card"><h3>Declare a window</h3><div class="body">
      <div class="row2">
        <div class="field"><label>Target</label><select id="mt"></select></div>
        <div class="field"><label>Title, shown publicly</label><input id="mtitle" placeholder="Router software upgrade"></div>
        <div class="field"><label>Starts</label><input id="mfrom" type="datetime-local" value="${toLocal(now + 3600)}"></div>
        <div class="field"><label>Ends</label><input id="mto" type="datetime-local" value="${toLocal(now + 3 * 3600)}"></div>
        <div class="field" style="grid-column:1/-1"><label>Note, shown publicly (optional)</label>
          <input id="mnote" placeholder="Provider window, nothing to do on your side">
          <small>The title and the note appear on the public page as written. Do not put anything there you
            would not publish: a supplier's name, a ticket reference, an internal address.</small></div>
      </div>
      <label class="chk"><input type="checkbox" id="malerts" checked><span>Silence alerting during the window
        <small>The incident is still recorded and still visible here, which is what explains the graph
          afterwards. Only the message is held back.</small></span></label>
      <label class="chk"><input type="checkbox" id="mprobe"><span>Stop measuring during the window
        <small>The target leaves the probe until the window closes, then comes back by itself. Use this when
          the work would read as total loss: the passes are never measured, so they cannot pollute the
          statistics, and the availability figure leaves the period out instead of counting it as downtime.
          The graph shows a gap, and the public banner says why it is there.</small></span></label>
      <button class="btn p" id="msave">Declare the window</button>
    </div></div>
    <div class="card"><h3>Windows</h3><div class="body" id="mlist">Loading…</div></div>`);
  try {
    const ts = await api("GET", "/api/v1/admin/targets");
    H.render($("#mt"), (ts || []).map(x => html`<option value="${x.id}">${x.title} — ${x.host}</option>`));
  } catch (e) { H.render($("#mt"), html`<option value="">${e.message}</option>`); }
  const load = async () => {
    try {
      const d = await api("GET", "/api/v1/admin/maintenance");
      const L = d.maintenances || [];
      if (!L.length) { H.render($("#mlist"), html`<div class="empty">No window declared. Declaring one takes less
        time than explaining a hole in a graph six months later.</div>`); return; }
      const t0 = Math.floor(Date.now() / 1000);
      H.render($("#mlist"), html`<table><thead><tr><th>Target</th><th>Window</th><th>Period</th><th>Effect</th><th></th></tr></thead><tbody>${L.map(x => {
          const live = x.starts_at <= t0 && x.ends_at > t0, past = x.ends_at <= t0;
          const state = live ? html`<span class="badge b-warn">in progress</span>`
            : past ? html`<span class="badge b-n">over</span>` : html`<span class="badge b-ok">planned</span>`;
          const eff = [x.stop_alerts ? "alerting silenced" : "", x.stop_probe ? "measurement stopped" : ""]
            .filter(Boolean).join(", ");
          return html`<tr><td>${x.target_title || ("#" + x.target_id)}</td>
            <td>${x.title}${x.note ? html`<div class="faint" style="font-size:11.5px">${x.note}</div>` : ""}</td>
            <td>${new Date(x.starts_at * 1000).toLocaleString()} → ${new Date(x.ends_at * 1000).toLocaleString()}
              <div>${state}</div></td>
            <td style="font-size:12.5px">${eff}</td>
            <td><button class="btn s" data-del="${x.id}">Delete</button></td></tr>`;
        })}</tbody></table>`);
      document.querySelectorAll("[data-del]").forEach(b => b.onclick = async () => {
        try { await api("DELETE", "/api/v1/admin/maintenance/" + b.dataset.del); toast("Deleted"); load(); }
        catch (e) { toast(e.message, true); }
      });
    } catch (e) { H.render($("#mlist"), html`<div class="note">${e.message}</div>`); }
  };
  $("#msave").onclick = async () => {
    try {
      await api("POST", "/api/v1/admin/maintenance", {
        target_id: parseInt($("#mt").value, 10), title: $("#mtitle").value, note: $("#mnote").value,
        starts_at: fromLocal($("#mfrom").value), ends_at: fromLocal($("#mto").value),
        stop_alerts: $("#malerts").checked, stop_probe: $("#mprobe").checked
      });
      toast("Declared"); $("#mtitle").value = ""; $("#mnote").value = ""; load();
    } catch (e) { toast(e.message, true); }
  };
  load();
}
