/* smokestack — HTML assembled without a hole to forget.
 *
 * Pages are built in the browser by concatenating strings, which puts an
 * escaping decision at every interpolation. There are over nine hundred
 * of them. Getting all nine hundred right once is possible; keeping them
 * right as the code changes is a different promise, and it is the kind
 * nobody can keep by attention alone.
 *
 *   H.html`<td>${t.title}</td>`          the title is escaped
 *   H.html`<tr>${rows.map(row)}</tr>`    nested templates are not
 *   H.html`<div>${H.raw(svg)}</div>`     saying so explicitly is the only way
 *
 * So the default is safe and the exception is written down. A reviewer
 * looks for H.raw, instead of reading every interpolation to decide
 * whether the value behind it could have come from a person.
 *
 * Values: null, undefined and false render as nothing, so a conditional
 * fragment is `cond && H.html`...`` with no empty-string branch. Numbers
 * and arrays do what they look like. Everything else is escaped.
 */
(function () {
  const CHARS = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };
  const esc = s => String(s == null ? "" : s).replace(/[&<>"']/g, c => CHARS[c]);

  // A marker, not a string: a string could come from anywhere, and the
  // point is that only this file can mint one.
  class Html {
    constructor(s) { this.s = s; }
    toString() { return this.s; }
  }

  const flatten = v => {
    if (v == null || v === false || v === true) return "";
    if (v instanceof Html) return v.s;
    if (Array.isArray(v)) return v.map(flatten).join("");
    return esc(v);
  };

  const html = (strings, ...values) =>
    new Html(strings.reduce((out, s, i) => out + flatten(values[i - 1]) + s));

  // Escaping is not enough for an href: javascript:… survives it and
  // stays clickable. Anything that is not plainly http(s) becomes empty.
  // The value comes back unescaped, because the template it lands in
  // escapes it; escaping twice would turn a & in a query into &amp;.
  const url = v => {
    const s = String(v == null ? "" : v).trim();
    return /^https?:\/\/[^\s]+$/i.test(s) ? s : "";
  };

  window.H = {
    html,
    esc,
    url,
    // Only for HTML this code built itself. Reviewing a call site means
    // answering one question: where did this string come from?
    raw: s => new Html(String(s == null ? "" : s)),
    // Assigning a template rather than a string keeps innerHTML from
    // accepting anything that merely looks like markup.
    render(el, tpl) {
      if (!el) return;
      el.innerHTML = tpl instanceof Html ? tpl.s : esc(tpl);
    },
    Html
  };
})();
