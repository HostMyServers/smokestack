/* smokestack — light or dark, decided before the first paint.
 *
 * Three states rather than two: "auto" follows the system and stays the
 * default, "light" and "dark" are an explicit choice the browser keeps.
 * A remembered choice lives on that machine only, like the language.
 *
 * This file is loaded from <head>, before the stylesheet, so the
 * attribute is set before anything is painted. Resolved any later, a
 * page would show light for a beat and then swap.
 */
(function () {
  const KEY = "smokestack.theme";
  const system = matchMedia("(prefers-color-scheme: dark)");

  // Private browsing can refuse storage. The choice then holds for this
  // page rather than being silently ignored.
  let choice = null;

  const stored = () => {
    try {
      const v = localStorage.getItem(KEY);
      return v === "light" || v === "dark" ? v : null;
    } catch (e) { return null; }
  };

  const apply = pref => {
    document.documentElement.dataset.theme =
      pref === "light" || pref === "dark" ? pref : system.matches ? "dark" : "light";
  };

  apply(stored());

  // The system can change while the page is open: an explicit choice
  // wins, "auto" follows.
  system.addEventListener("change", () => {
    if (stored() || choice) return;
    apply(null);
    document.dispatchEvent(new CustomEvent("theme:change"));
  });

  window.Theme = {
    // What was chosen, not what is displayed: data-theme says that.
    get pref() { return stored() || choice || "auto"; },
    set(pref) {
      choice = pref === "light" || pref === "dark" ? pref : null;
      try {
        if (choice) localStorage.setItem(KEY, choice);
        else localStorage.removeItem(KEY);
      } catch (e) { /* kept in memory for this page */ }
      apply(choice);
      document.dispatchEvent(new CustomEvent("theme:change"));
    }
  };
})();
