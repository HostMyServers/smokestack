/* smokestack back-office — response-time breakdown.
 * Charge par admin.html, dans l'ordre : ces fichiers partagent une
 * portee globale, comme app.js et i18n.js du site public. */
/* --------------------------------------------- response-time breakdown */
// A target that answers in eighty milliseconds does not say where those
// eighty milliseconds go, and a slow resolver and a slow network produce
// the same figure with different fixes. On demand rather than on every
// pass: the measurement costs a resolution and a full handshake, which is
// expensive to repeat every minute, and the latency series must keep its
// definition — this is a separate measurement beside it, not a change to
// what the graph means. Only the last result per target is kept.
async function viewBreak(m) {
  H.render(m, html`<h2>Response-time breakdown</h2>
    <p class="lead">Splits one connection into its steps: name resolution, TCP connection, and the TLS
      handshake where the port expects one. Run on demand, not on every pass — a handshake is far more
      expensive than a bare connection, and repeating it every minute would cost more than it explains.
      Nothing here is historised and nothing appears on the public pages: it is a diagnostic you look at
      when something is wrong, not a metric to follow, and it names the address actually reached.</p>
    <div class="card"><h3>Targets measured over TCP</h3><div class="body" id="bd">Loading…</div></div>`);
  const ms = us => us > 0 ? (us / 1000).toFixed(us < 10000 ? 2 : 1) : "—";
  // One bar per step, so the dominant step is visible before the numbers
  // are read. Widths are relative to this row's own total.
  const bar = b => {
    const tot = b.dns_us + b.connect_us + b.tls_us;
    if (tot <= 0) return "";
    const seg = (v, cls) => v > 0 ? html`<span class="bseg ${cls}" style="width:${(v * 100 / tot).toFixed(1)}%"></span>` : "";
    return html`<div class="bbar">${seg(b.dns_us, "b-dns")}${seg(b.connect_us, "b-conn")}${seg(b.tls_us, "b-tls")}</div>`;
  };
  const render = list => {
    if (!list.length) return html`<div class="empty">No target measured over TCP. A breakdown needs a
      connection to time, so an ICMP target has nothing to split.</div>`;
    return html`<table><thead><tr><th>Target</th><th>DNS</th><th>Connect</th><th>TLS</th><th>Total</th>
      <th>Address reached</th><th>Measured</th><th></th></tr></thead><tbody>${list.map(b => {
        const tot = b.dns_us + b.connect_us + b.tls_us;
        return html`<tr><td>${b.title}<div class="mono" style="font-size:11.5px;color:var(--ink2)">${b.host}:${b.port}</div>
          ${bar(b)}</td>
          <td class="mono">${ms(b.dns_us)}</td><td class="mono">${ms(b.connect_us)}</td>
          <td class="mono">${ms(b.tls_us)}</td><td class="mono"><strong>${ms(tot)}</strong></td>
          <td class="mono" style="font-size:11.5px">${b.ip || "—"}</td>
          <td class="faint" style="font-size:11.5px">${b.ts ? new Date(b.ts * 1000).toLocaleString() : "never"}</td>
          <td><button class="btn s" data-run="${b.target_id}">Measure</button></td></tr>`
          + (b.err ? html`<tr><td colspan="8" style="background:var(--bg2);color:var(--crit);font-size:12.5px">${b.err}</td></tr>` : "");
      })}</tbody></table>
      <div class="note">Milliseconds. <strong>DNS</strong> is a fresh lookup, not a cached one: measuring a
      cache tells you nothing. <strong>Connect</strong> is the SYN → SYN/ACK, taken from the kernel where it
      can be, so process load does not enter the figure. <strong>TLS</strong> is the handshake only, and the
      certificate is not verified here on purpose — its validity is the job of the certificate watch, which
      checks it properly. The total is not the number on the public graph: that one counts the connection
      alone, without resolution and without handshake.</div>`;
  };
  const wire = () => document.querySelectorAll("[data-run]").forEach(b => b.onclick = async () => {
    b.disabled = true; b.textContent = "…";
    try { await api("POST", "/api/v1/admin/breakdown/" + b.dataset.run, {}); load(); }
    catch (e) { toast(e.message, true); b.disabled = false; b.textContent = "Measure"; }
  });
  const load = async () => {
    try {
      const d = await api("GET", "/api/v1/admin/breakdown");
      H.render($("#bd"), render(d.breakdowns || [])); wire();
    } catch (e) { H.render($("#bd"), html`<div class="note">${e.message}</div>`); }
  };
  load();
}
