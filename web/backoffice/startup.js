/* smokestack back-office — boot.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* ----------------------------------------------------------- startup */
(async()=>{
  try{
    const st=await api("GET","/api/v1/auth/state");
    if(st.setup_required) return gateSetup();
    if(st.user){CSRF=st.csrf;ME=st.user;
      try{const p=await api("GET","/api/v1/admin/fed/pairing");
        PENDING=(p||[]).filter(x=>x.direction==="in"&&x.state==="pending").length;}catch(e){}
  try{const d=await api("GET","/api/v1/admin/messages");UNREAD=d.unread||0;}catch(e){}
      return shell();}
    gateLogin();
  }catch(e){
    $("#app").innerHTML=`<div class="gate"><div class="gatebox">
      <h1>Instance unreachable</h1><p class="sub">${esc(e.message)}</p></div></div>`;
  }
})();
