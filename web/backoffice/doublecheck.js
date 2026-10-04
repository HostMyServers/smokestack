/* smokestack back-office — federated double-check and messages.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ------------------------------------------------- federated double-check */
// One target failing from one vantage point does not say whether the target
// is down or our own transit is. A peer in another AS settles it in one
// pass. The whole difficulty is consent, not measurement: unguarded, this
// is a way to make somebody else's machine probe a third party under their
// AS number. So a peer may ask us only if we granted it in advance, only
// about an address it already publishes itself, and only within caps.
async function viewDC(m) {
  const VERDICT = {
    pending: ["b-n", "waiting for the first passes"],
    path: ["b-crit", "the peer reaches it, we do not — the fault is between us and the target"],
    target: ["b-crit", "neither of us reaches it — the target is down"],
    both_ok: ["b-ok", "the peer reaches it, and so do we"],
    intermittent: ["b-warn", "the peer reaches it only sometimes"]
  };
  H.render(m, html`<h2>Federated double-check</h2>
    <p class="lead">A second opinion on one of your targets, measured by a peer in another AS. If the peer
      reaches the target and you do not, the fault is between you and it; if neither of you reaches it, the
      target is down. Two AS, one conclusion, during the incident rather than after it.</p>
    <div class="note"><strong>What a peer may ask of you.</strong> Nothing, unless you grant it below —
      pairing is agreeing to exchange anchor measurements, not agreeing to probe on somebody's behalf. Once
      granted, individual requests need no human, which is the point: the answer is wanted at three in the
      morning. A request is still refused unless the address is already a <em>public</em> target on the
      requesting instance, which this instance verifies by reading that instance's own public API rather
      than taking its word. The measurements never touch your own series, your availability figures or your
      public pages, and they are deleted when the window closes.</p>
    <div class="card"><h3>What this instance does</h3><div class="body" id="dcset">Loading…</div></div>
    <div class="card"><h3>Peers you allow to ask you</h3><div class="body" id="dcgrants">Loading…</div></div>
    <div class="card"><h3>Ask a peer now</h3><div class="body" id="dcask">Loading…</div></div>
    <div class="card"><h3>Second opinions you asked for</h3><div class="body" id="dcout">Loading…</div></div>
    <div class="card"><h3>Measurements you are making for peers</h3><div class="body" id="dcin">Loading…</div></div>`);
  const when = ts => ts ? new Date(ts * 1000).toLocaleString() : "—";
  const left = ts => {
    const s = ts - Math.floor(Date.now() / 1000);
    return s > 0 ? Math.ceil(s / 60) + " min left" : "over";
  };
  const counts = c => c.passes ? `${c.passes - c.down}/${c.passes} answered` : "no pass yet";

  const load = async () => {
    let d;
    try { d = await api("GET", "/api/v1/admin/fed/dc"); }
    catch (e) { H.render($("#dcset"), html`<div class="note">${e.message}</div>`); return; }
    const s = d.settings || {}, caps = d.caps || {};

    H.render($("#dcset"), html`
      <label class="chk"><input type="checkbox" id="dc_auto" ${s.auto !== false ? "checked" : ""}>
        <span>Ask granted peers by myself when one of my targets opens an incident
          <small>On by default, and deliberately: a request is refused unless a peer granted it in advance,
            so this switch cannot cause anything a peer has not consented to. It only decides whether the
            answer arrives during the incident or only if somebody thinks to click.</small></span></label>
      <label class="chk"><input type="checkbox" id="dc_pub" ${s.public ? "checked" : ""}>
        <span>Show the corroboration on the target's public page
          <small>Off by default. The figure is useful to a reader, but it publishes a fact about a third
            party's reachability as measured by an operator who never agreed to have it published — so it
            is your deliberate choice, not a default. Nothing else about the check is ever published: no
            check identifier, no peer URL, no raw passes.</small></span></label>
      <div class="row2">
        <div class="field"><label>Peers asked per incident</label>
          <input id="dc_n" type="number" min="1" max="5" value="${s.ask_peers || 2}">
          <small>Two independent answers settle most incidents. More spends peers' daily allowances.</small></div>
        <div class="field"><label>Window asked for, in minutes</label>
          <input id="dc_min" type="number" min="1" max="${caps.max_minutes || 60}" value="${s.minutes || 15}">
          <small>Long enough to settle an incident, short enough not to be a monitoring job. The peer's own
            grant may cap it lower, and its refusal will say so.</small></div>
      </div>
      <button class="btn p" id="dc_save">Save</button>`);
    $("#dc_save").onclick = async () => {
      try {
        await api("PUT", "/api/v1/admin/fed/dc/settings", {
          auto: $("#dc_auto").checked, public: $("#dc_pub").checked,
          ask_peers: parseInt($("#dc_n").value, 10), minutes: parseInt($("#dc_min").value, 10)
        });
        toast("Saved");
      } catch (e) { toast(e.message, true); }
    };

    const G = d.grants || [], P = d.peers || [];
    const ungranted = P.filter(p => !G.some(g => g.peer_asn === p.asn));
    H.render($("#dcgrants"), [(G.length
      ? html`<table><thead><tr><th>Peer</th><th>Granted</th><th>Per day</th><th>At once</th>
          <th>Minutes</th><th>Now</th><th></th></tr></thead><tbody>${G.map(g => html`
          <tr><td>${g.peer_asn}${g.org ? html`<div class="faint" style="font-size:11.5px">${g.org}</div>` : ""}</td>
            <td>${when(g.granted_at)}${g.granted_by ? html`<div class="faint" style="font-size:11.5px">${g.granted_by}</div>` : ""}</td>
            <td><input class="gi" data-asn="${g.peer_asn}" data-k="max_per_day" type="number" min="1" max="${caps.max_per_day || 50}" value="${g.max_per_day}" style="width:5.5em"></td>
            <td><input class="gi" data-asn="${g.peer_asn}" data-k="max_concurrent" type="number" min="1" max="${caps.max_concurrent || 10}" value="${g.max_concurrent}" style="width:5.5em"></td>
            <td><input class="gi" data-asn="${g.peer_asn}" data-k="max_minutes" type="number" min="1" max="${caps.max_minutes || 60}" value="${g.max_minutes}" style="width:5.5em"></td>
            <td style="font-size:12.5px">${g.running || 0} running<div class="faint">${g.used_today || 0} today</div></td>
            <td><button class="btn s" data-save="${g.peer_asn}">Save</button>
                <button class="btn s" data-revoke="${g.peer_asn}">Revoke</button></td></tr>`)}</tbody></table><div class="note">Revoking takes effect on the next request <em>and</em> stops
            whatever that peer has running here: a revocation that left running checks in place would be a
            revocation in name only.</div>`
      : html`<div class="empty">No peer may ask this instance for measurements. That is the shipped state, and
          it is the right one until you decide otherwise.</div>`),
      (ungranted.length
        ? html`<div style="margin-top:12px;display:flex;gap:7px;align-items:end;flex-wrap:wrap">
            <div class="field" style="margin:0"><label>Grant an approved peer</label>
              <select id="dc_newasn">${ungranted.map(p => html`<option value="${p.asn}">${p.asn} — ${p.org || ""}</option>`)}</select></div>
            <button class="btn" id="dc_grant">Grant</button></div>`
        : (P.length ? "" : html`<div class="note" style="margin-top:12px">No approved peer yet. Pair with one in
            <a href="#" data-goto="fed">Peers and pairing</a> first: an AS number is declared, never proved,
            so granting a stranger would grant whoever turns up with that number.</div>`))]);

    document.querySelectorAll("[data-save]").forEach(b => b.onclick = async () => {
      const asn = b.dataset.save, body = {};
      document.querySelectorAll(`.gi[data-asn="${asn}"]`).forEach(i => body[i.dataset.k] = parseInt(i.value, 10));
      try { await api("PUT", "/api/v1/admin/fed/dc/grants/" + encodeURIComponent(asn), body); toast("Saved"); load(); }
      catch (e) { toast(e.message, true); }
    });
    document.querySelectorAll("[data-revoke]").forEach(b => b.onclick = async () => {
      try { await api("DELETE", "/api/v1/admin/fed/dc/grants/" + encodeURIComponent(b.dataset.revoke)); toast("Revoked"); load(); }
      catch (e) { toast(e.message, true); }
    });
    const gb = $("#dc_grant");
    if (gb) gb.onclick = async () => {
      try {
        await api("PUT", "/api/v1/admin/fed/dc/grants/" + encodeURIComponent($("#dc_newasn").value), {});
        toast("Granted"); load();
      } catch (e) { toast(e.message, true); }
    };
    document.querySelectorAll("[data-goto]").forEach(a => a.onclick = e => { e.preventDefault(); go(a.dataset.goto); });

    // Asking. Only public targets are offered, because that is the rule the
    // peer applies on its side and a local refusal is a better message.
    let targets = [];
    try { targets = (await api("GET", "/api/v1/admin/targets")).filter(t => t.public); }
    catch (e) { targets = []; }
    H.render($("#dcask"), !P.length
      ? html`<div class="empty">No approved peer to ask.</div>`
      : !targets.length
        ? html`<div class="empty">No public target. A double-check only covers a target this instance
            publishes itself — that is the rule the peer checks, so a private target cannot be
            corroborated this way.</div>`
        : html`<div style="display:flex;gap:9px;align-items:end;flex-wrap:wrap">
            <div class="field" style="margin:0"><label>Target</label><select id="dc_t">${targets.map(t =>
              html`<option value="${t.id}">${t.title} — ${t.host}</option>`)}</select></div>
            <div class="field" style="margin:0"><label>Peer</label><select id="dc_p">${P.map(p =>
              html`<option value="${p.asn}">${p.asn} — ${p.org || ""}</option>`)}</select></div>
            <div class="field" style="margin:0"><label>Minutes</label>
              <input id="dc_m" type="number" min="1" max="${caps.max_minutes || 60}" value="${s.minutes || 15}" style="width:6em"></div>
            <button class="btn p" id="dc_go">Ask</button></div>
          <div class="note">The peer decides. A refusal names its reason — no grant, a cap reached, or an
            address it cannot find among your public targets — and is shown here as it was sent.</div>`);
    const go2 = $("#dc_go");
    if (go2) go2.onclick = async () => {
      go2.disabled = true;
      try {
        await api("POST", "/api/v1/admin/fed/dc/ask", { target_id: parseInt($("#dc_t").value, 10),
          asn: $("#dc_p").value, minutes: parseInt($("#dc_m").value, 10) });
        toast("Asked"); load();
      } catch (e) { toast(e.message, true); }
      finally { go2.disabled = false; }
    };

    const rows = (list, dir) => !list.length
      ? html`<div class="empty">${dir === "out" ? "No second opinion asked for in the last seven days."
          : "No peer has asked this instance to measure anything in the last seven days."}</div>`
      : html`<table><thead><tr>${dir === "out"
            ? html`<th>Target</th><th>Peer</th>`
            : html`<th>Asked by</th><th>Address</th>`}
          <th>Window</th><th>Result</th>${dir === "out" ? html`<th>Verdict</th>` : ""}
          <th>State</th></tr></thead><tbody>${list.map(c => {
          const v = VERDICT[c.verdict] || ["b-n", c.verdict || ""];
          return html`<tr>
            ${dir === "out"
              ? html`<td>${c.target_title || ("#" + c.local_target_id)}</td>
                 <td>${c.peer_asn}${c.peer_org ? html`<div class="faint" style="font-size:11.5px">${c.peer_org}</div>` : ""}</td>`
              : html`<td>${c.peer_asn}${c.peer_org ? html`<div class="faint" style="font-size:11.5px">${c.peer_org}</div>` : ""}</td>
                 <td class="mono" style="font-size:11.5px">${c.host}${c.port ? ":" + c.port : ""} · ${c.proto}</td>`}
            <td style="font-size:12.5px">${when(c.started_at)}<div class="faint">${c.state === "running" ? left(c.expires_at) : "closed"}</div></td>
            <td style="font-size:12.5px">${counts(c)}${c.med_ms ? html`<div class="faint">median ${c.med_ms.toFixed(1)} ms</div>` : ""}</td>
            ${dir === "out" ? html`<td><span class="badge ${v[0]}">${v[1]}</span></td>` : ""}
            <td>${c.state === "running" ? html`<span class="badge b-warn">running</span>` : html`<span class="badge b-n">done</span>`}
              ${c.note ? html`<div class="faint" style="font-size:11px">${c.note}</div>` : ""}</td></tr>`;
        })}</tbody></table>`;
    H.render($("#dcout"), rows(d.outgoing || [], "out"));
    H.render($("#dcin"), [rows(d.incoming || [], "in"),
      html`<div class="note">These are measurements this instance makes for somebody else. They are kept out
          of your series, your availability figures and your public pages, and both the temporary target and
          every pass it produced are deleted when the window closes. Listed here because consent given in
          advance must not mean invisible: you answer for what your instance measures.</div>`]);
  };
  load();
}

async function viewMessages(m){
  H.render(m,html`<h2>Messages</h2>
    <p class="lead">Messages sent from the contact form of the public “About” page. Your address is
      never published there. Reply from your own mail client: the instance never writes on your behalf.
      Enable or disable the form in <a href="#" data-goto="site">Publisher page</a>.</p>
    <div class="card"><div class="body" id="mlist">Loading…</div></div>`);
  document.querySelectorAll("[data-goto]").forEach(a=>a.onclick=e=>{e.preventDefault();go(a.dataset.goto);});
  try{
    const d=await api("GET","/api/v1/admin/messages");
    const L=d.messages||[]; UNREAD=d.unread||0;
    H.render($("#mlist"),L.length?L.map(x=>html`<div class="msg" style="padding:12px 0;border-top:1px solid var(--line2)">
      <div style="display:flex;gap:10px;align-items:baseline;flex-wrap:wrap">
        ${x.read?"":html`<span class="badge b-warn">new</span>`}
        <strong>${x.name}</strong>
        <a href="mailto:${x.email}?subject=${encodeURIComponent("Re: "+(x.subject||"your message"))}">${x.email}</a>
        <span class="faint" style="font-size:12px">${new Date(x.ts*1000).toLocaleString()}</span>
        ${x.subject?html`<span>— ${x.subject}</span>`:""}
        <span class="faint mono" style="font-size:11.5px;margin-left:auto">${x.ip}</span>
      </div>
      <div style="white-space:pre-wrap;margin:8px 0 6px;font-size:13.5px">${x.body}</div>
      <div class="acts">
        <a class="btn s" href="mailto:${x.email}?subject=${encodeURIComponent("Re: "+(x.subject||"your message"))}">Reply</a>
        <button class="btn s" data-read="${x.id}" data-v="${x.read?0:1}">${x.read?"Mark unread":"Mark read"}</button>
        <button class="btn d s" data-mdel="${x.id}">Delete</button></div></div>`):
      html`<div class="empty">No message yet.</div>`);
    document.querySelectorAll("[data-read]").forEach(b=>b.onclick=async()=>{
      try{await api("PATCH","/api/v1/admin/messages/"+b.dataset.read,{read:b.dataset.v==="1"});render();}
      catch(e){toast(e.message,true);}});
    document.querySelectorAll("[data-mdel]").forEach(b=>b.onclick=async()=>{
      if(!confirm("Delete this message?")) return;
      try{await api("DELETE","/api/v1/admin/messages/"+b.dataset.mdel);toast("Message deleted");render();}
      catch(e){toast(e.message,true);}});
  }catch(e){H.render($("#mlist"),html`<div class="note">${e.message}</div>`);}
}

async function viewUsers(m){
  H.render(m,html`<h2>Users</h2>
    <p class="lead"><strong>master</strong> manages accounts and everything else.
      <strong>admin</strong> manages configuration, federation and storage, but not accounts.
      <strong>editor</strong> only manages targets and categories. <strong>viewer</strong> is read-only.
      At least one active master account must always remain.</p>
    <div class="card"><h3>New account</h3><div class="body"><div class="row2">
      <div class="field"><label>Email</label><input id="ue" type="email"></div>
      <div class="field"><label>Display name</label><input id="un"></div>
      <div class="field"><label>Password</label><input id="up" type="password"></div>
      <div class="field"><label>Role</label><select id="ur">
        <option value="viewer">viewer</option><option value="editor">editor</option>
        <option value="admin">admin</option><option value="master">master</option></select></div>
    </div><button class="btn p" id="add">Create the account</button></div></div>
    <div class="card"><h3>Accounts</h3><div class="body" id="l">Loading…</div></div>`);
  $("#add").onclick=async()=>{
    try{
      await api("POST","/api/v1/admin/users",{email:$("#ue").value,name:$("#un").value,
        password:$("#up").value,role:$("#ur").value});
      toast("Account created");render();
    }catch(e){toast(e.message,true);}
  };
  try{
    const list=await api("GET","/api/v1/admin/users");
    H.render($("#l"),html`<table><thead><tr><th>Email</th><th>Name</th><th>Role</th>
      <th>Last sign-in</th><th></th></tr></thead><tbody>${list.map(u=>html`<tr class="${u.disabled?"dis":""}">
        <td>${u.email}${u.id===ME.id?html` <span class="badge b-n">you</span>`:""}</td>
        <td>${u.display_name}</td>
        <td><select data-role="${u.id}" style="width:auto;padding:3px 7px">${["viewer","editor","admin","master"].map(r=>html`<option ${u.role===r?"selected":""}>${r}</option>`)}</select></td>
        <td style="font-size:12px">${dt(u.last_login_at)}</td>
        <td style="text-align:right;white-space:nowrap">
          <button class="btn s" data-dis="${u.id}" data-on="${u.disabled?0:1}">${u.disabled?"Enable":"Disable"}</button>
          <button class="btn s" data-rst="${u.id}">Reset password</button>
          ${u.id!==ME.id?html`<button class="btn d s" data-udel="${u.id}">Delete</button>`:""}
        </td></tr>`)}</tbody></table>`);
    document.querySelectorAll("[data-role]").forEach(s=>s.onchange=async()=>{
      try{await api("PATCH","/api/v1/admin/users/"+s.dataset.role,{role:s.value});
        toast("Role changed");render();}catch(e){toast(e.message,true);render();}
    });
    document.querySelectorAll("[data-dis]").forEach(b=>b.onclick=async()=>{
      try{await api("PATCH","/api/v1/admin/users/"+b.dataset.dis,{disabled:b.dataset.on==="1"});
        toast("Account updated");render();}catch(e){toast(e.message,true);}
    });
    document.querySelectorAll("[data-rst]").forEach(b=>b.onclick=async()=>{
      const pw=prompt("New password (12 characters minimum)");if(!pw) return;
      try{await api("PATCH","/api/v1/admin/users/"+b.dataset.rst,{password:pw});
        toast("Password reset, sessions closed");}catch(e){toast(e.message,true);}
    });
    document.querySelectorAll("[data-udel]").forEach(b=>b.onclick=async()=>{
      if(!confirm("Delete this account?")) return;
      try{await api("DELETE","/api/v1/admin/users/"+b.dataset.udel);
        toast("Account deleted");render();}catch(e){toast(e.message,true);}
    });
  }catch(e){H.render($("#l"),html`<div class="note">${e.message}</div>`);}
}
