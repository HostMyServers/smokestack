/* smokestack back-office — traceroutes.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ------------------------------------------------------- traceroutes */
const KIND_BADGE={anomaly:"b-crit",reference:"b-ok",manual:"b-n"};
function hopRTT(h){
  if(!h.rtt_ms||!h.rtt_ms.length) return "* * *";
  const v=h.rtt_ms,avg=v.reduce((a,b)=>a+b,0)/v.length;
  return `${avg.toFixed(2)} ms <span style="color:var(--ink3)">(${Math.min(...v).toFixed(2)}–${Math.max(...v).toFixed(2)})</span>`;
}
// Reaching the NOC of a network seen in a traceroute, from what that network
// declares in PeeringDB, with a mail already written: during an incident the
// time goes into finding the address and explaining the context, not into
// the diagnosis itself.
let SITE_CACHE=null;
async function siteInfo(){
  if(!SITE_CACHE){ try{ SITE_CACHE=await api("GET","/api/v1/site"); }catch(e){ SITE_CACHE={}; } }
  return SITE_CACHE;
}

async function nocPanel(asn, ctx){
  const box=document.createElement("div");
  box.className="note"; box.style.margin="8px 0";
  box.innerHTML=`Looking up ${esc(asn)} in PeeringDB…`;
  try{
    const d=await api("GET","/api/v1/admin/asn-contact?asn="+encodeURIComponent(asn));
    if(d.pending){
      box.innerHTML=`${esc(asn)} — being fetched from PeeringDB, try again in a moment.`;
      return box;
    }
    const cs=d.contacts||[];
    const site=await siteInfo();
    const subject=`${site.asn?site.asn+" ":""}→ ${asn}: ${ctx.problem} towards ${ctx.dest}`;
    // null drops a line, "" keeps a paragraph break: filtering on "" would
    // glue the whole message into one block.
    const body=[
      `Hello,`, "",
      `${site.org||site.title||"We"}${site.asn?" ("+site.asn+")":""} measure ${ctx.problem} on the path`,
      `towards ${ctx.dest}, which transits your network.`, "",
      `Measured from ${site.location||"our network"}, ${ctx.when}.`,
      ctx.hop?`The hop involved: ${ctx.hop}`:null, "",
      ctx.share?`Our graph, its percentiles and the traceroutes taken during the degradation, `
        +`read-only: ${ctx.share}`
        :`We can share our graph and the traceroutes taken during the degradation.`, "",
      `Thanks for having a look,`, "",
      `${site.noc_email?site.noc_email:(site.org||"")}`
    ].filter(x=>x!==null).join("\n");
    const mail=cs.find(c=>c.email);
    box.innerHTML=`<strong>${esc(d.name||d.holder||asn)}</strong> · ${esc(asn)}
      ${d.policy?` · peering policy: ${esc(d.policy)}`:""}
      <div style="margin-top:6px">${cs.length?cs.map(c=>`<div>
        <span class="badge ${c.role==="NOC"?"b-ok":"b-n"}">${esc(c.role||"contact")}</span>
        ${c.name?esc(c.name)+" — ":""}
        ${c.email?`<a href="mailto:${esc(c.email)}" class="mono">${esc(c.email)}</a>`:""}
        ${c.phone?` · <span class="mono">${esc(c.phone)}</span>`:""}</div>`).join("")
        :`<em>This network declares no public contact in PeeringDB.</em> Its page may show more to a
          signed-in user, and an API key in the instance settings would fetch it.`}</div>
      <div class="acts" style="margin-top:8px">
        ${mail?`<a class="btn p s" href="mailto:${esc(mail.email)}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}">Write to the NOC</a>`:""}
        <button class="btn s" data-copy>Copy the message</button>
        ${ctx.makeShare?`<button class="btn s" data-mkshare title="Create a read-only link to this target and put it in the message">Attach a share link</button>`:""}
        <a class="btn s" href="${esc(d.peeringdb_url||"")}" target="_blank" rel="noreferrer">PeeringDB page</a>
        ${d.looking_glass?`<a class="btn s" href="${esc(d.looking_glass)}" target="_blank" rel="noreferrer">Looking glass</a>`:""}
      </div>`;
    let text=body;
    const cp=box.querySelector("[data-copy]");
    if(cp) cp.onclick=()=>{navigator.clipboard?.writeText(subject+"\n\n"+text);toast("Message copied");};
    const sb=box.querySelector("[data-mkshare]");
    if(sb) sb.onclick=async()=>{
      sb.disabled=true; sb.textContent="Creating…";
      try{
        const url=await ctx.makeShare();
        text=text.replace("We can share our graph and the traceroutes taken during the degradation.",
          "Our graph, its percentiles and the traceroutes taken during the degradation, read-only "
          +"(valid 30 days): "+url);
        const a=box.querySelector("a.btn.p");
        if(a) a.href="mailto:"+encodeURIComponent(mail.email).replace(/%40/,"@")
          +"?subject="+encodeURIComponent(subject)+"&body="+encodeURIComponent(text);
        sb.outerHTML=`<span class="faint" style="font-size:11.5px">share link added to the message ·
          revoke it in Share links</span>`;
        toast("Share link created and added to the message");
      }catch(e){ toast(e.message,true); sb.disabled=false; sb.textContent="Attach a share link"; }
    };
  }catch(e){ box.innerHTML=`<span style="color:var(--crit)">${esc(e.message)}</span>`; }
  return box;
}

function hopsTable(tr,ref){
  const rows=[],n=Math.max(tr.hops.length,ref?ref.hops.length:0);
  for(let i=0;i<n;i++){
    const h=tr.hops[i],r=ref?ref.hops[i]:null;
    const differs=ref&&r&&r.addr&&(!h||(h.addr||"")!==r.addr);
    rows.push(`<tr class="${differs?"diff":""}"><td>${i+1}</td>
      <td class="mono">${h?esc(h.addr||"*"):""}${h&&h.note?`<span class="hopnote" title="the router refused to forward">${esc(h.note)}</span>`:""}</td>
      <td style="font-size:12px">${h?esc(h.name||""):""}</td>
      <td class="mono" style="font-size:12px">${h&&h.asn?`${esc(h.asn)}
        <button class="btn s asnoc" data-noc="${esc(h.asn)}" title="How to reach this network's NOC, as it declares it in PeeringDB">NOC</button>`:""}</td>
      <td style="font-size:12px">${h?hopRTT(h):""}</td>
      <td style="font-size:12px">${h?`${(h.rtt_ms||[]).length}/${h.sent}`:""}</td>
      ${ref?`<td class="mono" style="font-size:12px;color:var(--ink2)">${r?esc(r.addr||"*"):"—"}</td>`:""}</tr>`);
  }
  return `<table style="margin-top:6px"><thead><tr><th>Hop</th><th>Address</th><th>Name</th><th>AS</th>
    <th>RTT (avg, min–max)</th><th>Replies</th>${ref?`<th>Reference path, ${dt(ref.ts)}</th>`:""}</tr></thead>
    <tbody>${rows.join("")}</tbody></table>
    ${ref?`<div style="font-size:12px;color:var(--ink2);margin-top:6px">Highlighted rows: a router of the last healthy path is different or no longer answers.</div>`:""}`;
}
// One point per traceroute: a jump in the number of hops, or a path that
// stops reaching the destination, is a topology change you can see.
function hopChart(pts){
  if(!pts||pts.length<2) return `<div class="empty">Not enough traceroutes yet.</div>`;
  const W=760,H=120,P=24;
  const max=Math.max(...pts.map(p=>p.hops),1), min=Math.min(...pts.map(p=>p.hops),max);
  const lo=Math.max(0,min-1), hi=max+1;
  const x=i=>P+i*(W-2*P)/Math.max(1,pts.length-1);
  const y=v=>H-P-(v-lo)*(H-2*P)/Math.max(1,hi-lo);
  const line=pts.map((p,i)=>`${i?"L":"M"}${x(i).toFixed(1)},${y(p.hops).toFixed(1)}`).join("");
  const dots=pts.map((p,i)=>`<circle cx="${x(i).toFixed(1)}" cy="${y(p.hops).toFixed(1)}" r="${p.kind==="reference"?2.5:3.5}"
     fill="${p.reached?(p.kind==="anomaly"?"#dc2626":"#0ea5e9"):"#f59e0b"}">
     <title>${esc(new Date(p.ts*1000).toLocaleString())} — ${p.hops} hops, ${esc(p.kind)}${p.reached?"":", not reached"}${p.as_path?" — "+esc(p.as_path):""}</title></circle>`).join("");
  return `<svg viewBox="0 0 ${W} ${H}" style="width:100%;height:${H}px">
    <path d="${line}" fill="none" stroke="#0ea5e9" stroke-width="1.5"/>${dots}
    <text x="4" y="${y(hi).toFixed(1)}" font-size="10" fill="#94a3b8">${hi}</text>
    <text x="4" y="${y(lo).toFixed(1)}" font-size="10" fill="#94a3b8">${lo}</text>
  </svg>
  <div style="font-size:11.5px;color:var(--ink2);margin-top:4px">${pts.length} traceroutes over 30 days ·
    blue: destination reached · amber: not reached · red: taken during an anomaly ·
    hover a point for its AS path</div>`;
}

async function viewTraces(m){
  m.innerHTML=`<h2>Traceroutes</h2>
    <p class="lead">The probe runs a traceroute by itself when a target leaves its baseline
      (packet loss above 3 %, or median above 1.4 × its usual value), at most once every
      15 minutes per target and 30 times per hour overall. It also records a reference path
      once a day while the target is healthy, so an anomaly can be compared with a known-good
      path. Traceroutes are shown on public pages only if enabled in <a href="#" id="tosite">Publisher page</a>.</p>
    <div class="card"><div class="body">
      <div class="row2" style="align-items:end">
        <div class="field" style="margin:0"><label>Target</label><select id="trt"></select></div>
        <div style="display:flex;gap:7px"><button class="btn p" id="trrun">Run a traceroute now</button>
          <button class="btn" id="trref">Refresh</button></div>
      </div></div></div>
    <div class="card"><h3>Hops over time</h3><div class="body" id="hopg">Loading…</div></div>
    <div class="tabs" id="trtabs"></div>
    <div class="card"><div class="body" id="trl">Loading…</div></div>`;
  $("#tosite").onclick=e=>{e.preventDefault();go("site");};
  const targets=await api("GET","/api/v1/admin/targets").catch(()=>[]);
  if(!targets||!targets.length){$("#trl").innerHTML=`<div class="empty">No target yet.</div>`;return;}
  if(!TR_TARGET||!targets.some(t=>t.id===TR_TARGET)) TR_TARGET=targets[0].id;
  $("#trt").innerHTML=targets.map(t=>`<option value="${t.id}" ${t.id===TR_TARGET?"selected":""}>${esc(t.title)} — ${esc(t.host)}</option>`).join("");
  let kind="", list=[], open=null;
  // Reloaded on every target change: the chart described the first target
  // whatever was selected.
  const loadHops=()=>{
    if($("#hopg")) $("#hopg").innerHTML="Loading…";
    api("GET","/api/v1/admin/hops?target="+TR_TARGET+"&days=30")
      .then(p=>{ if($("#hopg")) $("#hopg").innerHTML=hopChart(p); })
      .catch(e=>{ if($("#hopg")) $("#hopg").innerHTML=`<div class="note">${esc(e.message)}</div>`; });
  };
  const draw=()=>{
    $("#trtabs").innerHTML=[["","All"],["anomaly","Anomalies"],["reference","References"],["manual","Manual"]]
      .map(([k,l])=>`<button data-k="${k}" aria-pressed="${kind===k}">${l} (${k?list.filter(x=>x.kind===k).length:list.length})</button>`).join("");
    document.querySelectorAll("[data-k]").forEach(b=>b.onclick=()=>{kind=b.dataset.k;draw();});
    const shown=kind?list.filter(x=>x.kind===kind):list;
    if(!shown.length){$("#trl").innerHTML=`<div class="empty">No traceroute yet for this target.</div>`;return;}
    $("#trl").innerHTML=`<table><thead><tr><th>When</th><th>Kind</th><th>Reason</th><th>Destination</th>
      <th>Result</th></tr></thead><tbody>`+shown.map(tr=>{
        const last=tr.hops.length, lastHop=tr.hops[last-1]||{};
        const result=tr.reached?`<span class="badge b-ok">reached in ${last} hops</span>`
          :lastHop.note?`<span class="badge b-crit">refused at hop ${last} ${esc(lastHop.note)}</span>`
          :`<span class="badge b-warn">not reached</span>`;
        const ref=tr.kind==="reference"?null:list.find(x=>x.kind==="reference"&&x.ts<tr.ts);
        return `<tr class="trrow" data-tr="${tr.id}"><td style="font-size:12px;white-space:nowrap">${dt(tr.ts)}</td>
          <td><span class="badge ${KIND_BADGE[tr.kind]||"b-n"}">${esc(tr.kind)}</span></td>
          <td style="font-size:12px">${esc(tr.reason)}</td>
          <td class="mono" style="font-size:12px">${esc(tr.dest)} <span style="color:var(--ink3)">IPv${tr.family}</span></td>
          <td>${result}</td></tr>`+
          (open===tr.id?`<tr><td colspan="5" style="background:var(--panel)">${hopsTable(tr,ref)}</td></tr>`:"");
      }).join("")+`</tbody></table>`;
    document.querySelectorAll("[data-tr]").forEach(r=>r.onclick=()=>{const id=+r.dataset.tr;open=open===id?null:id;draw();});
    // Contacting the NOC of a hop's network, with the incident's context
    // already in the message.
    document.querySelectorAll("[data-noc]").forEach(b=>b.onclick=async e=>{
      e.stopPropagation();
      const tr=list.find(x=>x.id===open)||{};
      const tgt=targets.find(t=>t.id===TR_TARGET)||{};
      const row=b.closest("tr");
      if(row.nextElementSibling&&row.nextElementSibling.dataset.nocrow){ row.nextElementSibling.remove(); return; }
      const holder=document.createElement("tr"); holder.dataset.nocrow="1";
      const cell=document.createElement("td"); cell.colSpan=7; holder.appendChild(cell);
      row.parentNode.insertBefore(holder,row.nextSibling);
      cell.appendChild(await nocPanel(b.dataset.noc,{
        problem: (tr.kind==="anomaly"?"packet loss or added latency":"a path change"),
        dest: (tgt.title?tgt.title+" ("+(tgt.host||"")+")":(tr.dest||"our target")),
        when: tr.ts?new Date(tr.ts*1000).toISOString().replace("T"," ").slice(0,16)+" UTC":"recently",
        hop: b.closest("tr").querySelector("td.mono")?.textContent.trim()||"",
        // Creating the link on the spot: an existing token is never shown
        // again, so the only way to put one in the mail is to make one.
        makeShare: async () => {
          const l=await api("POST","/api/v1/admin/targets/"+TR_TARGET+"/share",
            {days:30,note:"sent to "+b.dataset.noc});
          return l.url;
        }
      }));
    });
  };
  const load=async()=>{
    try{list=await api("GET","/api/v1/admin/traceroutes?target="+TR_TARGET+"&limit=100");
      if(open===null&&list.length) open=(list.find(x=>x.kind!=="reference")||list[0]).id; draw();}
    catch(e){$("#trl").innerHTML=`<div class="note">${esc(e.message)}</div>`;}
  };
  loadHops();
  $("#trt").onchange=()=>{TR_TARGET=+$("#trt").value;open=null;load();loadHops();};
  $("#trref").onclick=()=>{load();loadHops();};
  $("#trrun").onclick=async()=>{
    try{await api("POST","/api/v1/admin/traceroutes",{target_id:TR_TARGET});
      toast("Traceroute requested: the result appears in about 10 seconds");
      setTimeout(load,9000);setTimeout(load,16000);}
    catch(e){toast(e.message,true);}
  };
  load();
}
