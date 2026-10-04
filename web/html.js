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
 * Values: null, undefined and both booleans render as nothing, so a
 * conditional fragment is `cond && H.html`...`` with no empty-string
 * branch. A boolean *attribute* therefore spells its value out —
 * aria-checked="${a === b ? "true" : "false"}" — rather than relying on
 * a bare boolean, which would print nothing at either end. Numbers and
 * arrays do what they look like. Everything else is escaped.
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
    if (v == null || typeof v === "boolean") return "";
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
    // accepting anything that merely looks like markup. A list of
    // templates goes through the same path as one nested in a template,
    // because a caller that builds rows with .map() should not have to
    // know it is handing over an array.
    render(el, tpl) {
      if (!el) return;
      el.innerHTML = flatten(tpl);
    },
    // Replacing an element rather than filling one. Without it a caller
    // reaches for outerHTML, which takes a string and so takes anything:
    // the point of this file is that there is one way in, not two.
    // <template> parses the fragment in a context of its own, so a <tr>
    // survives the trip where a bare <div> would have dropped it.
    replace(el, tpl) {
      if (!el || !el.parentNode) return;
      const slot = document.createElement("template");
      slot.innerHTML = flatten(tpl);
      el.replaceWith(slot.content);
    },
    Html
  };
})();
