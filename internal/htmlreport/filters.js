(() => {
  "use strict";
  const form = document.getElementById("filters");
  if (!form) return;
  const rows = [...document.querySelectorAll("[data-finding]")];
  const facets = ["severity", "namespace", "workload", "resource"];
  for (const key of facets) {
    const select = form.elements.namedItem(key);
    const values = [...new Set(rows.map(row => row.dataset[key]).filter(Boolean))].sort();
    for (const value of values) select.add(new Option(value, value));
  }
  function apply() {
    let count = 0;
    for (const row of rows) {
      row.hidden = facets.some(key => {
        const value = form.elements.namedItem(key).value;
        return value && value !== row.dataset[key];
      });
      if (!row.hidden) count++;
    }
    document.getElementById("filter-count").textContent = count + " of " + rows.length + " findings";
  }
  form.addEventListener("change", apply);
  form.addEventListener("reset", () => setTimeout(apply));
  form.hidden = false;
  apply();
  // A finding reached from the relationship or investigation index stays accessible.
  function revealHash() {
    const target = document.getElementById(location.hash.slice(1));
    if (!target) return;
    const row = target.closest("[data-finding]");
    if (row && row.hidden) { form.reset(); apply(); }
    if (target.tagName === "DETAILS") target.open = true;
  }
  window.addEventListener("hashchange", revealHash);
  revealHash();
})();
