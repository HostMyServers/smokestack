/* smokestack back-office — first-time setup and sign-in.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* --------------------------------------------------- entry screens */
function gateSetup(){
  H.render($("#app"),html`<div class="gate"><div class="gatebox">
    <div class="logo">S</div>
    <h1>First-time setup</h1>
    <p class="sub">No account exists yet. Create the master account: it can manage
      users and the whole configuration.</p>
    <div class="field"><label>Setup code</label><input id="c" class="mono" placeholder="XXXX-XXXX-XXXX" autocomplete="off">
      <div style="font-size:11.5px;color:var(--ink2);margin-top:4px">Shown in the service log
        (<span class="mono">journalctl -u smokestack</span>) and in <span class="mono">/var/lib/smokestack/setup-code</span>.</div></div>
    <div class="field"><label>Email address</label><input id="e" type="email" autocomplete="username"></div>
    <div class="field"><label>Display name</label><input id="n" type="text"></div>
    <div class="field"><label>Password (12 characters minimum)</label>
      <input id="p" type="password" autocomplete="new-password"></div>
    <button class="btn p" id="go" style="width:100%">Create the master account</button>
  </div></div>`);
  const go=async()=>{
    try{
      const r=await api("POST","/api/v1/auth/setup",
        {setup_code:$("#c").value,email:$("#e").value,name:$("#n").value,password:$("#p").value});
      CSRF=r.csrf;ME=r.user;shell();
    }catch(e){toast(e.message,true);}
  };
  $("#go").onclick=go;
  $("#p").onkeydown=e=>{if(e.key==="Enter")go();};
}
function gateLogin(){
  H.render($("#app"),html`<div class="gate"><div class="gatebox">
    <div class="logo">S</div>
    <h1>Administration</h1>
    <p class="sub">Sign in to access the back-office.</p>
    <div class="field"><label>Email address</label><input id="e" type="email" autocomplete="username"></div>
    <div class="field"><label>Password</label><input id="p" type="password" autocomplete="current-password"></div>
    <button class="btn p" id="go" style="width:100%">Sign in</button>
    <p class="sub" style="margin:16px 0 0;font-size:12px">
      <a href="/" style="color:var(--blue)">← Back to the public graphs</a></p>
  </div></div>`);
  const go=async()=>{
    try{
      const r=await api("POST","/api/v1/auth/login",{email:$("#e").value,password:$("#p").value});
      CSRF=r.csrf;ME=r.user;shell();
    }catch(e){toast(e.message,true);}
  };
  $("#go").onclick=go;
  $("#p").onkeydown=e=>{if(e.key==="Enter")go();};
}
