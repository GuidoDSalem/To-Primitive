"use strict";

// Busy: a small donut at the top left that fills as whatever is being
// processed advances, with the percent in the middle. It follows the page's
// own progress bars (every visible <progress> not yet full) and whatever a
// page reports with Busy.set(key, fraction, label) / Busy.done(key); a
// fraction of null means "working on it", without a percent. A page with
// <body data-busy="beside"> has it next to the settings panel instead, at
// the top of what it shows (the reel editor: between settings and reel).

const Busy = (() => {
  const tasks = new Map(); // key -> { fraction, label }
  const R = 15, C = 2 * Math.PI * R;
  let box, ring, text, hideTimer, queued = false;

  function mount() {
    box = document.createElement("div");
    box.id = "busy";
    box.setAttribute("role", "progressbar");
    box.innerHTML =
      `<svg viewBox="0 0 40 40" aria-hidden="true"><circle class="track" cx="20" cy="20" r="${R}"/>` +
      `<circle class="fill" cx="20" cy="20" r="${R}" stroke-dasharray="${C}" stroke-dashoffset="${C}"/></svg><span></span>`;
    ring = box.querySelector(".fill");
    text = box.querySelector("span");
    document.body.append(box);
    const panel = document.getElementById("panel");
    if (document.body.dataset.busy === "beside" && panel) {
      box.classList.add("beside");
      const place = () => { box.style.left = panel.getBoundingClientRect().right + 14 + "px"; };
      new ResizeObserver(place).observe(panel);
      addEventListener("resize", place);
      place();
    }
    new MutationObserver((ms) => {
      if (ms.some((m) => !box.contains(m.target))) update();
    }).observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["value", "max", "hidden", "open"] });
    update();
  }

  // What a bar is about: its title, or the row or field it sits in.
  function labelOf(p) {
    const row = p.closest(".job, .field, section");
    return p.title || row?.querySelector(".what, label, h2")?.textContent.trim() || "";
  }

  function current() {
    const out = [...tasks.values()];
    for (const p of document.querySelectorAll("progress")) {
      if (!p.hasAttribute("value") || p.closest("[hidden]") || box.contains(p)) continue;
      const f = p.max > 0 ? p.value / p.max : 0;
      if (f < 1) out.push({ fraction: f, label: labelOf(p) });
    }
    return out;
  }

  function update() {
    if (queued || !box) return;
    queued = true;
    requestAnimationFrame(() => {
      queued = false;
      draw(current());
    });
  }

  function draw(list) {
    const known = list.filter((t) => t.fraction != null);
    if (!list.length) {
      if (box.classList.contains("on") && !hideTimer) {
        // Finished: it shows full for a moment before going away.
        show(1, false);
        hideTimer = setTimeout(() => {
          hideTimer = null;
          box.classList.remove("on");
          document.documentElement.classList.remove("busy-on");
        }, 700);
      }
      return;
    }
    clearTimeout(hideTimer);
    hideTimer = null;
    const fraction = known.length ? known.reduce((s, t) => s + t.fraction, 0) / known.length : null;
    show(fraction, !known.length);
    const labels = [...new Set(list.map((t) => t.label).filter(Boolean))];
    box.title = (list.length > 1 ? `${list.length} procesos` : "Procesando") + (labels.length ? ": " + labels.join(" · ") : "");
    box.classList.add("on");
    if (!box.classList.contains("beside")) document.documentElement.classList.add("busy-on");
  }

  function show(fraction, spinning) {
    box.classList.toggle("spin", spinning);
    const f = spinning ? 0.25 : Math.max(0, Math.min(1, fraction));
    ring.setAttribute("stroke-dashoffset", C * (1 - f));
    text.textContent = spinning ? "…" : Math.floor(f * 100) + "%";
    box.setAttribute("aria-valuenow", spinning ? "" : Math.floor(f * 100));
  }

  if (document.body) mount();
  else document.addEventListener("DOMContentLoaded", mount);

  return {
    set(key, fraction, label = "") {
      tasks.set(key, { fraction: fraction == null ? null : Math.max(0, Math.min(1, fraction)), label });
      update();
    },
    done(key) {
      if (tasks.delete(key)) update();
    },
    // track shows a task while a promise runs.
    track(key, promise, label = "") {
      this.set(key, null, label);
      return promise.finally(() => this.done(key));
    },
  };
})();
