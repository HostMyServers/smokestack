/* smokestack back-office — remote probe enrolment.
 * Loaded by admin.html in order: these files share a global scope, like app.js and i18n.js on the public site. */
/* ------------------------------------------------------------- probes */
async function viewProbes(m){
  H.render(m,html`<h2>Probes</h2>
    <p class="lead">A remote probe measures from another location and authenticates to this instance with a token.
      The local probe writes through the Unix socket and takes no token. A token is shown once, when it is issued;
      only its hash is stored, so it cannot be shown again. Rotating a token issues a new one and the old one stops working.</p>
    <div id="pl">Loading…</div>
    <div class="card"><h3>Add a probe</h3><div class="body">
      <div class="field"><label>Slug</label><input id="pslug" class="mono" placeholder="paris-1" autocomplete="off"></div>
      <div class="field"><label>Name</label><input id="pname" placeholder="Paris 1" autocomplete="off"></div>
      <div class="field"><label>Location</label><input id="ploc" placeholder="Paris, FR" autocomplete="off"></div>
      <button class="btn p" id="padd">Add probe</button>
      <div class="note" style="margin-top:12px">The slug takes lowercase letters, digits and inner hyphens, 2 to 39 characters.
        A new probe has no token and cannot send anything until you issue one.</div>
    </div></div>`);

  // The token lives only in the argument of this call and in the DOM it renders. It is never stored anywhere else, and the next render drops it.
  //
  // It is built separately from the list on purpose. The server has already stored the hash by the time we get here, so a token that is not shown
  // is a token nobody will ever know, and the probe would have to be rotated again to recover. Reloading the list must therefore never be able to
  // swallow it: the block is rendered whether that reload succeeds or fails.
  const tokenCard=issued=>issued?html`<div class="card" style="border-color:var(--crit)"><h3>Token for ${issued.slug}</h3><div class="body">
        <p style="margin:0 0 10px"><strong>Copy this token now.</strong> It is shown once and will not be shown again,
          because only its hash is stored. If you lose it, rotate the token to issue a new one.</p>
        <div class="mono" style="padding:10px 12px;background:var(--line2);border-radius:var(--radius-s);word-break:break-all;user-select:all">${issued.token}</div>
      </div></div>`:"";

  const load=async(issued)=>{
    let list;
    try{list=(await api("GET","/api/v1/admin/probes")).probes||[];}
    catch(e){H.render($("#pl"),html`${tokenCard(issued)}<div class="note">${e.message}</div>`);return;}
    const row=p=>html`<tr>
      <td class="mono">${p.slug}</td><td>${p.name}</td><td>${p.location||"—"}</td>
      <td>${dt(p.last_seen_at)}</td>
      <td>${p.local?html`<span class="badge b-n">local</span> `:""}
        ${p.enabled?html`<span class="badge b-ok">enabled</span>`:html`<span class="badge b-warn">disabled</span>`}
        ${p.local?"":p.has_token?html` <span class="badge b-ok">token set</span>`:html` <span class="badge b-crit">no token</span>`}</td>
      <td class="acts">${p.local?"":html`
        <button class="btn s" data-tok="${p.id}" data-slug="${p.slug}" data-had="${p.has_token?"1":"0"}">${p.has_token?"Rotate token":"Issue token"}</button>
        ${p.has_token?html`<button class="btn d s" data-rev="${p.id}" data-slug="${p.slug}">Revoke token</button>`:""}
        <button class="btn s" data-en="${p.id}" data-to="${p.enabled?"0":"1"}">${p.enabled?"Disable":"Enable"}</button>`}</td>
      </tr>`;
    H.render($("#pl"),html`
      ${tokenCard(issued)}
      <div class="card"><h3>Registered probes</h3><div class="body">
        ${list.length?html`<table><thead><tr><th>Slug</th><th>Name</th><th>Location</th><th>Last seen</th><th>State</th><th></th></tr></thead>
          <tbody>${list.map(row)}</tbody></table>`:html`<div class="empty">No probe.</div>`}
      </div></div>`);
    document.querySelectorAll("[data-tok]").forEach(b=>b.onclick=async()=>{
      if(b.dataset.had==="1"&&!confirm("Rotate the token of "+b.dataset.slug+"? The current token stops working immediately.")) return;
      try{
        const r=await api("POST","/api/v1/admin/probes/"+b.dataset.tok+"/token");
        await load({slug:b.dataset.slug,token:r.token});
      }catch(e){toast(e.message,true);}
    });
    document.querySelectorAll("[data-rev]").forEach(b=>b.onclick=async()=>{
      if(!confirm("Revoke the token of "+b.dataset.slug+"? The probe can no longer send measurements until a new token is issued.")) return;
      try{await api("DELETE","/api/v1/admin/probes/"+b.dataset.rev+"/token");toast("Token revoked");await load();}
      catch(e){toast(e.message,true);}
    });
    document.querySelectorAll("[data-en]").forEach(b=>b.onclick=async()=>{
      const on=b.dataset.to==="1";
      try{await api("PATCH","/api/v1/admin/probes/"+b.dataset.en,{enabled:on});toast(on?"Probe enabled":"Probe disabled");await load();}
      catch(e){toast(e.message,true);}
    });
  };

  $("#padd").onclick=async()=>{
    try{
      await api("POST","/api/v1/admin/probes",{slug:$("#pslug").value,name:$("#pname").value,location:$("#ploc").value});
      ["#pslug","#pname","#ploc"].forEach(s=>$(s).value="");
      toast("Probe added");await load();
    }catch(e){toast(e.message,true);}
  };
  await load();
}
