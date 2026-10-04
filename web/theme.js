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

    // The state the button moves to. A fixed auto -> light -> dark order
    // looked right and was not: on a machine set to light, leaving "auto"
    // for "light" changed nothing on screen, so the first press appeared
    // to do nothing and the page only turned dark on the second.
    //
    // From "auto" it therefore goes to the opposite of what is on screen.
    // Automatic comes back at the end of the round, which is the one place
    // where a press that changes only the button is not a surprise: it is
    // the press that hands the decision back to the system.
    next() {
      const shown = document.documentElement.dataset.theme === "dark" ? "dark" : "light";
      const pref = this.pref;
      if (pref === "auto") return shown === "dark" ? "light" : "dark";
      if (pref === (system.matches ? "dark" : "light")) return "auto";
      return pref === "dark" ? "light" : "dark";
    },
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
