/* smokestack back-office — storage and retention.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ----------------------------------------------------------- storage */
async function viewStorage(m){
  m.innerHTML=`<h2>Storage</h2>
    <p class="lead">Hourly and daily aggregates are never evicted. Only the raw archive is
      rotated, and chunks already stored on S3 go first since they remain retrievable.</p>
    <div id="s">Loading…</div>`;
  try{
    const d=await api("GET","/api/v1/admin/storage");
    const c=d.config,r=d.report||{},s3=c.s3||{},lo=c.local||{};
    $("#s").innerHTML=`<div class="card"><h3>Mode</h3><div class="body">
      <div class="field"><label>Policy</label><select id="mode">
        <option value="local" ${c.mode==="local"?"selected":""}>Local only (capacity-based rotation)</option>
        <option value="s3" ${c.mode==="s3"?"selected":""}>S3 first</option>
        <option value="hybrid" ${c.mode==="hybrid"?"selected":""}>Hybrid (local, then S3)</option>
      </select></div></div></div>
      <div class="card"><h3>S3 target</h3><div class="body"><div class="row2">
        <div class="field"><label>Endpoint</label><input id="ep" value="${esc(s3.endpoint||"")}" placeholder="s3.fr-par.scw.cloud"></div>
        <div class="field"><label>Bucket</label><input id="bk" value="${esc(s3.bucket||"")}"></div>
        <div class="field"><label>Prefix</label><input id="px" value="${esc(s3.prefix||"")}"></div>
        <div class="field"><label>Region</label><input id="rg" value="${esc(s3.region||"")}"></div>
        <div class="field"><label>Access key</label><input id="ak" value="${esc(s3.access_key||"")}"></div>
        <div class="field"><label>Secret key</label><input id="sk" type="password" placeholder="unchanged"></div>
        <div class="field"><label>Storage class</label><input id="cl" value="${esc(s3.storage_class||"")}"></div>
        <label class="chk"><input type="checkbox" id="ps" ${s3.path_style?"checked":""}><span>Path-style URLs (MinIO, Ceph)</span></label>
      </div></div></div>
      <div class="card"><h3>Local rotation</h3><div class="body"><div class="row2">
        <div class="field"><label>Quota (GB)</label><input id="q" type="number" step="0.5"
          value="${((lo.quota_bytes||0)/1073741824).toFixed(1)}"></div>
        <div class="field"><label>High watermark</label><input id="hw" type="number" step="0.01" value="${lo.high_watermark||0.85}"></div>
        <div class="field"><label>Low watermark</label><input id="lw" type="number" step="0.01" value="${lo.low_watermark||0.7}"></div>
        <div class="field"><label>Keep locally (hours)</label><input id="kl" type="number" value="${lo.keep_local_hours||48}"></div>
      </div>
      <button class="btn p" id="save">Save</button></div></div>
      <div class="card"><h3>Status</h3><div class="body"><table><tbody>
        <tr><td style="width:210px">Local archive</td><td>${gb(r.local_bytes||0)} / ${gb(r.quota_bytes||0)}</td></tr>
        <tr><td>Disk usage</td><td>${((r.disk_used_pct||0)*100).toFixed(1)} %
          ${r.degraded?'<span class="badge b-crit">degraded mode</span>':""}</td></tr>
        <tr><td>Chunks</td><td>${r.chunks||0}</td></tr>
        <tr><td>Pending uploads</td><td>${r.pending_uploads||0}</td></tr>
        <tr><td>Projected retention</td><td>${r.projected_days?r.projected_days.toFixed(0)+" days":"—"}</td></tr>
      </tbody></table></div></div>`;
    document.querySelectorAll("[data-goto]").forEach(a=>a.onclick=e=>{e.preventDefault();go(a.dataset.goto);});
    $("#save").onclick=async()=>{
      try{
        await api("PUT","/api/v1/admin/storage",{
          mode:$("#mode").value,
          s3:{endpoint:$("#ep").value,bucket:$("#bk").value,prefix:$("#px").value,
              region:$("#rg").value,access_key:$("#ak").value,secret_key:$("#sk").value,
              path_style:$("#ps").checked,storage_class:$("#cl").value},
          local:{quota_bytes:Math.round(parseFloat($("#q").value||0)*1073741824),
                 high_watermark:parseFloat($("#hw").value),
                 low_watermark:parseFloat($("#lw").value),
                 keep_local_hours:parseInt($("#kl").value,10)}});
        toast("Saved");render();
      }catch(e){toast(e.message,true);}
    };
  }catch(e){$("#s").innerHTML=`<div class="note">${esc(e.message)}</div>`;}
}
