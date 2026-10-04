/* smokestack back-office — notification channels and alert rules.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ------------------------------------------------------------- users */
const CHAN_KINDS=[
  ["smtp","Email (SMTP server)"],["sendmail","Email (local sendmail)"],
  ["webhook","Webhook (JSON)"],["slack","Slack"],["teams","Microsoft Teams"],
  ["telegram","Telegram"],["twilio","Twilio (SMS or WhatsApp)"],
  ["ovh_sms","OVHcloud SMS"],["gatewayapi","GatewayAPI (SMS)"]];
const CHAN_FIELDS={
  smtp:[["host","Server","mail.example.net"],["port","Port","587"],["security","Security (starttls, tls, none)","starttls"],
        ["user","Username",""],["pass","Password",""],["from","Sender","smokestack@example.net"],["to","Recipients","noc@example.net, astreinte@example.net"]],
  sendmail:[["command","Command","/usr/sbin/sendmail"],["from","Sender","smokestack@example.net"],["to","Recipients","noc@example.net"]],
  webhook:[["url","URL","https://hooks.example.net/…"]],
  slack:[["url","Incoming webhook URL","https://hooks.slack.com/services/…"]],
  teams:[["url","Webhook URL (connector or Workflows)","https://…"]],
  telegram:[["token","Bot token",""],["chat","Chat id","-1001234567890"]],
  twilio:[["account_sid","Account SID","AC…"],["auth_token","Auth token",""],
          ["from_number","Sender","+33100000000 or whatsapp:+3315…"],["to_numbers","Recipients","+33600000000"]],
  ovh_sms:[["app_key","Application key",""],["app_secret","Application secret",""],["consumer_key","Consumer key",""],
           ["service_name","SMS service","sms-ab1234-1"],["sender","Sender (optional)",""],["to_numbers","Recipients","+33600000000"]],
  gatewayapi:[["token","API token",""],["sender","Sender (optional)","smokestack"],["to_numbers","Recipients","+33600000000"]]};

async function viewChannels(m){
  m.innerHTML=`<h2>Notification channels</h2>
    <p class="lead">Where alerts go: email through your own SMTP server or the local sendmail, a chat room,
      or SMS. Every channel gets the alerts of <em>My targets alerting</em> and of the federation NOC.
      Chat and SMS receive a shortened message — a traceroute is unreadable on a phone — while email and
      webhook carry the whole thing. <strong>Test each channel</strong>: one that is never tested is one
      that fails the night it matters.</p>
    <div id="chans"></div>
    <div class="acts"><select id="newkind">${CHAN_KINDS.map(([k,l])=>`<option value="${k}">${esc(l)}</option>`).join("")}</select>
      <button class="btn" id="addchan">Add this channel</button>
      <button class="btn p" id="savechans">Save</button></div>`;
  let SET={channels:[]};
  const draw=()=>{
    $("#chans").innerHTML=SET.channels.length?SET.channels.map((c,i)=>`
      <div class="card"><h3>${esc(CHAN_KINDS.find(k=>k[0]===c.kind)?.[1]||c.kind)}</h3><div class="body">
        <div class="row2">
          <div class="field"><label>Name</label><input data-f="${i}:name" value="${esc(c.name||"")}"></div>
          ${(CHAN_FIELDS[c.kind]||[]).map(([f,label,ph])=>`<div class="field"><label>${esc(label)}</label>
            <input data-f="${i}:${f}" value="${esc(c[f]==null?"":String(c[f]))}" placeholder="${esc(ph)}"></div>`).join("")}
        </div>
        <label class="chk"><input type="checkbox" data-f="${i}:enabled" ${c.enabled?"checked":""}><span>Enabled</span></label>
        <div class="acts"><button class="btn s" data-test="${i}">Send a test message</button>
          <button class="btn d s" data-rm="${i}">Remove</button></div>
      </div></div>`).join(""):`<div class="empty">No channel yet. Alerts have nowhere to go.</div>`;
    document.querySelectorAll("[data-f]").forEach(el=>el.onchange=()=>{
      const [i,f]=el.dataset.f.split(":");
      SET.channels[i][f]= el.type==="checkbox" ? el.checked : (f==="port"? parseInt(el.value||"0",10) : el.value);
    });
    document.querySelectorAll("[data-rm]").forEach(b=>b.onclick=()=>{SET.channels.splice(+b.dataset.rm,1);draw();});
    document.querySelectorAll("[data-test]").forEach(b=>b.onclick=async()=>{
      const c=SET.channels[+b.dataset.test];
      if(!c.id){toast("Save first, then test",true);return;}
      b.disabled=true;b.textContent="Sending…";
      try{await api("POST","/api/v1/admin/channels/test",{id:c.id});toast("Test message sent through "+(c.name||c.kind));}
      catch(e){toast(e.message,true);}
      b.disabled=false;b.textContent="Send a test message";
    });
  };
  try{ SET=await api("GET","/api/v1/admin/channels")||{channels:[]}; SET.channels=SET.channels||[]; }catch(e){}
  draw();
  $("#addchan").onclick=()=>{SET.channels.push({kind:$("#newkind").value,enabled:true,
    name:CHAN_KINDS.find(k=>k[0]===$("#newkind").value)[1]});draw();};
  $("#savechans").onclick=async()=>{
    try{await api("PUT","/api/v1/admin/channels",SET);toast("Channels saved");render();}
    catch(e){toast(e.message,true);}
  };
}

async function viewAlerts(m){
  m.innerHTML=`<h2>Alerts on your own targets</h2>
    <p class="lead">Warns you when one of <em>your</em> targets stays in incident. Different from the
      federation alerting above, which warns a peer's NOC about their network.
      A traceroute is taken the moment the incident opens, and its path — compared with the last
      healthy one — travels in the message, so you know where it breaks before opening anything.</p>
    <div class="card"><div class="body" id="al">Loading…</div></div>
    <div class="card"><h3>Recent incidents</h3><div class="body" id="alinc">Loading…</div></div>`;
  try{
    const d=await api("GET","/api/v1/admin/alerts"); const c=d.config||{};
    $("#al").innerHTML=`
      <label class="chk"><input type="checkbox" id="al_en" ${c.enabled?"checked":""}>
        <span>Alert me when a target stays in incident<small>Off by default. This is the global switch;
          each target can also be left out, with the <em>Alerts</em> button in
          <a href="#" data-goto="targets">Targets and categories</a>.</small></span></label>
      <div class="row2">
        <div class="field"><label>After how long (minutes)</label>
          <input id="al_min" type="number" min="1" max="1440" value="${c.after_minutes||5}"></div>
        <div class="field"><label>Silence between two alerts for one target (hours)</label>
          <input id="al_rep" type="number" min="1" max="168" value="${c.repeat_hours||6}"></div>
        <div class="field"><label>Recipients (never public)</label>
          <input id="al_to" value="${esc(c.recipients||"")}" placeholder="noc@example.net, astreinte@example.net"></div>
        <div class="field"><label>Webhook (optional)</label>
          <input id="al_hook" value="${esc(c.webhook_url||"")}" placeholder="https://hooks.example.net/…"></div>
      </div>
      <label class="chk"><input type="checkbox" id="al_rec" ${c.recovery!==false?"checked":""}>
        <span>Also tell me when it is over</span></label>
      <div class="note">Alerts leave through the channels configured in
        <a href="#" data-goto="channels">Notification channels</a> (SMTP, chat, SMS), plus the
        addresses and webhook below if you set them.</div>
      <button class="btn p" id="al_save">Save</button>`;
    document.querySelectorAll("[data-goto]").forEach(a=>a.onclick=e=>{e.preventDefault();go(a.dataset.goto);});
    $("#al_save").onclick=async()=>{
      try{await api("PUT","/api/v1/admin/alerts",{enabled:$("#al_en").checked,
        after_minutes:parseInt($("#al_min").value,10),repeat_hours:parseInt($("#al_rep").value,10),
        recipients:$("#al_to").value,webhook_url:$("#al_hook").value,recovery:$("#al_rec").checked});
        toast("Saved");}catch(e){toast(e.message,true);}
    };
    const L=d.incidents||[];
    $("#alinc").innerHTML=L.length?`<table><thead><tr><th>Target</th><th>Opened</th><th>Lasted</th>
      <th>Alerted</th><th>State</th></tr></thead><tbody>`+L.map(i=>{
      const end=i.closed_at||Math.floor(Date.now()/1000);
      const mins=Math.max(0,Math.round((end-i.opened_at)/60));
      return `<tr><td>${esc(i.title||("#"+i.target_id))}<div class="mono" style="font-size:11.5px;color:var(--ink2)">${esc(i.host||"")}</div></td>
        <td>${new Date(i.opened_at*1000).toLocaleString()}</td>
        <td>${mins} min</td>
        <td>${i.notified_at?'<span class="badge b-warn">sent</span>':'<span class="badge b-n">no</span>'}</td>
        <td>${i.closed_at?'<span class="badge b-ok">closed</span>':'<span class="badge b-crit">open</span>'}</td></tr>`;
    }).join("")+`</tbody></table>`:`<div class="empty">No incident recorded yet.</div>`;
  }catch(e){$("#al").innerHTML=`<div class="note">${esc(e.message)}</div>`;}
}
