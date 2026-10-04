/* smokestack back-office — updates.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ----------------------------------------------------------- updates */
async function viewUpdate(m){
  H.render(m,html`<h2>Updates</h2>
    <p class="lead">Upload the ZIP of a new version: it is verified (signature, platform,
      checksum), tested, then activated in one click. The previous version stays on disk and
      takes over automatically if the new one fails to start.</p><div id="u">Loading…</div>`);
  let S;
  try{S=await api("GET","/api/v1/admin/update");}catch(e){H.render($("#u"),html`<div class="note">${e.message}</div>`);return;}
  const staged=S.staged, av=S.available;
  const h=[html`<div class="card"><h3>Installed version</h3><div class="body"><table><tbody>
    <tr><td style="width:220px">Version</td><td><strong class="mono">${S.version}</strong> <span style="color:var(--ink3)">${S.platform} ${S.build_date||""}</span></td></tr>
    <tr><td>Previous version</td><td class="mono">${S.previous||"—"}</td></tr>
    <tr><td>In-place updates</td><td>${S.managed?html`<span class="badge b-ok">available</span> <span class="mono" style="font-size:12px">`+S.root+html`</span>`
      :html`<span class="badge b-crit">unavailable</span> ${S.reason}`}</td></tr>
    <tr><td>Release keys</td><td class="mono">${S.trusted_keys.length?S.trusted_keys.join(", "):html`<span style="color:var(--crit)">none</span>`}
      ${S.allow_unsigned?html` <span class="badge b-warn">unsigned packages allowed</span>`:""}</td></tr>
    </tbody></table>
    ${S.previous&&S.managed?html`<div class="acts"><button class="btn d s" id="rb">Revert to ${S.previous}</button></div>`:""}
  </div></div>`];
  h.push(html`<div class="card"><h3>Install a version</h3><div class="body">
    <div class="field"><label>ZIP package (smokestack-VERSION-linux-ARCH.zip)</label>
      <input type="file" id="pkg" accept=".zip,application/zip" ${S.managed?"":"disabled"}></div>
    <button class="btn p" id="up" ${S.managed?"":"disabled"}>Verify the package</button>
    <div id="stg" style="margin-top:14px"></div></div></div>`);
  h.push(html`<div class="card"><h3>Automatic updates</h3><div class="body">
    <label class="chk"><input type="checkbox" id="ac" ${S.auto_check?"checked":""}><span>Check regularly for new versions
      <small>Source: <span class="mono">${S.manifest_url}</span></small></span></label>
    <div class="field" style="max-width:320px"><label>How often to check</label>
      <select id="ch">${[[6,"Every 6 hours"],[24,"Every day (default)"],[168,"Every week"],[720,"Every month"]]
        .map(([h,l])=>html`<option value="${h}" ${S.check_interval_hours===h?"selected":""}>${l}</option>`)}
        ${[6,24,168,720].includes(S.check_interval_hours)?"":html`<option value="${S.check_interval_hours}" selected>Every ${S.check_interval_hours} hours (config.json)</option>`}
      </select>
      <div style="font-size:11.5px;color:var(--ink2);margin-top:4px">A skipped version is never a problem:
        the newest release is installed directly, whatever the current version.</div></div>
    <label class="chk"><input type="checkbox" id="aa" ${S.auto_apply?"checked":""} ${S.managed?"":"disabled"}><span>Install new versions automatically
      <small>Only packages signed by a trusted key are installed automatically, without exception.</small></span></label>
    <div class="acts"><button class="btn s" id="chk">Check now</button></div>
    <div style="margin-top:10px;font-size:13px">${av?(av.newer?html`<span class="badge b-warn">version ${av.version} available</span>
      <button class="btn p s" id="inow" style="margin-left:8px">Install it now</button>`
      :html`<span class="badge b-ok">up to date</span> latest published version: ${av.version}`):""}</div>
  </div></div>`);
  const ACT={update:"update",rollback:"rollback",auto_rollback:html`<span class="badge b-crit">automatic rollback</span>`};
  h.push(html`<div class="card"><h3>History</h3><div class="body">${(S.history.length?html`<table><thead><tr><th>Date</th><th>Action</th><th>From</th><th>To</th><th>By</th></tr></thead><tbody>${S.history.map(x=>html`<tr><td>${dt(x.ts)}</td><td>${ACT[x.action]||x.action}</td><td class="mono">${x.from||"—"}</td>
      <td class="mono">${x.to}</td><td>${x.by}</td></tr>`)}</tbody></table>`:html`<div class="empty">No update yet.</div>`)}</div></div>`);
  H.render($("#u"),h);

  const showStaged=st=>{H.render($("#stg"),html`<div class="pend">
    <div style="display:flex;gap:9px;align-items:baseline;flex-wrap:wrap"><strong>Version ${st.version}</strong>
      ${st.signed?html`<span class="badge b-ok">signed · key ${st.key_id}</span>`:html`<span class="badge b-warn">unsigned</span>`}
      ${st.newer?html`<span class="badge b-ok">newer</span>`:html`<span class="badge b-warn">not newer than ${st.current}</span>`}</div>
    <div style="font-size:12px;color:var(--ink2);margin-top:4px">${st.os}-${st.arch} · built ${st.built_at||"?"}
      · SHA-256 <span class="mono">${st.sha256.slice(0,16)}…</span> · self-test passed</div>
    ${st.notes?html`<div class="quote">${st.notes}</div>`:""}
    ${st.newer?"":html`<label class="chk"><input type="checkbox" id="frc"><span>Force installing an older or identical version</span></label>`}
    <div class="acts"><button class="btn p" id="ap">Install and restart</button></div></div>`);
    $("#ap").onclick=async()=>{
      if(!confirm("Install version "+st.version+"? The service restarts (a few seconds).")) return;
      try{await api("POST","/api/v1/admin/update/apply",{force:!!($("#frc")&&$("#frc").checked)});waitRestart(st.version);}
      catch(e){toast(e.message,true);}
    };
  };
  if(staged) showStaged(staged);
  if($("#up")) $("#up").onclick=async()=>{
    const f=$("#pkg").files[0]; if(!f){toast("Choose a ZIP file",true);return;}
    const fd=new FormData(); fd.append("package",f);
    $("#up").disabled=true;$("#up").textContent="Verifying…";
    try{
      const r=await fetch("/api/v1/admin/update/upload",{method:"POST",body:fd,headers:{"X-CSRF-Token":CSRF},credentials:"same-origin"});
      const d=await r.json(); if(!r.ok) throw new Error(d.error||("HTTP "+r.status));
      showStaged(d);
    }catch(e){toast(e.message,true);}
    $("#up").disabled=false;$("#up").textContent="Verify the package";
  };
  if($("#rb")) $("#rb").onclick=async()=>{
    if(!confirm("Revert to version "+S.previous+"?")) return;
    try{await api("POST","/api/v1/admin/update/rollback");waitRestart(S.previous);}catch(e){toast(e.message,true);}
  };
  const save=async()=>{try{await api("PUT","/api/v1/admin/update/settings",
      {auto_check:$("#ac").checked,auto_apply:$("#aa").checked,check_interval_hours:parseInt($("#ch").value,10)});
      toast("Preferences saved");}
    catch(e){toast(e.message,true);}};
  $("#ac").onchange=save;$("#aa").onchange=save;$("#ch").onchange=save;
  $("#chk").onclick=async()=>{try{await api("POST","/api/v1/admin/update/check");render();}catch(e){toast(e.message,true);}};
  if($("#inow")) $("#inow").onclick=async()=>{
    if(!confirm("Install version "+av.version+" now? The service restarts (a few seconds).")) return;
    try{await api("POST","/api/v1/admin/update/install-latest");waitRestart(av.version);}
    catch(e){toast(e.message,true);}
  };
}
async function waitRestart(ver){
  H.render($("#u"),html`<div class="card"><div class="body"><strong>Restarting on version ${ver}…</strong>
    <p class="lead" style="margin-top:6px">This page reloads by itself.</p></div></div>`);
  for(let i=0;i<60;i++){
    await new Promise(r=>setTimeout(r,1500));
    try{const v=await fetch("/api/v1/version",{cache:"no-store"}).then(r=>r.json());
      if(v.version===ver){toast("Version "+ver+" is running");render();return;}}catch(e){}
  }
  toast("The service is not answering yet: check journalctl -u smokestack",true);
}
