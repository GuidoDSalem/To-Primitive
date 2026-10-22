"use strict";

// Busy: a donut at the top left that fills as whatever is being processed
// advances, with the percent in the middle and, beside it, about how long is
// left. It follows the page's own progress bars (every visible <progress> not
// yet full) and whatever a page reports with Busy.set(key, fraction, label) /
// Busy.done(key); a fraction of null means "working on it", without a
// percent. A page with <body data-busy="beside"> has it next to the settings
// panel instead, at the top of what it shows (the reel editor: between
// settings and reel).

const Busy = (() => {
  const tasks = new Map(); // key -> { fraction, label }
  const R = 26, C = 2 * Math.PI * R;
  let box, ring, pct, eta, hideTimer, tick, queued = false;
  // Recent (time, fraction) of what's being followed, for the estimate; it
  // starts over when what's followed changes.
  let samples = [], followed = "";

  function mount() {
    box = document.createElement("div");
    box.id = "busy";
    box.setAttribute("role", "progressbar");
    box.innerHTML =
      `<div class="ring"><svg viewBox="0 0 61 61" aria-hidden="true"><circle class="track" cx="30.5" cy="30.5" r="${R}"/>` +
      `<circle class="fill" cx="30.5" cy="30.5" r="${R}" stroke-dasharray="${C}" stroke-dashoffset="${C}"/></svg>` +
      `<span class="pct"></span></div><span class="eta" hidden></span>`;
    ring = box.querySelector(".fill");
    pct = box.querySelector(".pct");
    eta = box.querySelector(".eta");
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
    const out = [...tasks.entries()].map(([key, t]) => ({ ...t, key }));
    for (const p of document.querySelectorAll("progress")) {
      if (!p.hasAttribute("value") || p.closest("[hidden]") || box.contains(p)) continue;
      const f = p.max > 0 ? p.value / p.max : 0;
      if (f < 1) out.push({ fraction: f, label: labelOf(p), key: "bar:" + labelOf(p) });
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
      clearInterval(tick);
      tick = null;
      samples = [];
      if (box.classList.contains("on") && !hideTimer) {
        // Finished: it shows full for a moment before going away.
        show(1, false);
        eta.hidden = true;
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
    // While it's on, the estimate keeps up even if nothing moves.
    tick ??= setInterval(update, 1000);
    const fraction = known.length ? known.reduce((s, t) => s + t.fraction, 0) / known.length : null;
    show(fraction, !known.length);
    showEta(known, fraction);
    const labels = [...new Set(list.map((t) => t.label).filter(Boolean))];
    box.title = (list.length > 1 ? `${list.length} procesos` : "Procesando") + (labels.length ? ": " + labels.join(" · ") : "");
    box.classList.add("on");
    if (!box.classList.contains("beside")) {
      document.documentElement.classList.add("busy-on");
      document.documentElement.style.setProperty("--busy-space", box.offsetWidth + 24 + "px");
    }
  }

  function show(fraction, spinning) {
    box.classList.toggle("spin", spinning);
    const f = spinning ? 0.25 : Math.max(0, Math.min(1, fraction));
    ring.setAttribute("stroke-dashoffset", C * (1 - f));
    pct.textContent = spinning ? "…" : Math.floor(f * 100) + "%";
    box.setAttribute("aria-valuenow", spinning ? "" : Math.floor(f * 100));
  }

  // The estimate: the pace of the last half minute or so, carried to the
  // end. It shows once there are a few seconds of progress to go by.
  function showEta(known, fraction) {
    const now = performance.now();
    const what = known.map((t) => t.key).sort().join("|");
    if (what !== followed || (samples.length && fraction < samples.at(-1).f - 0.01)) {
      followed = what;
      samples = [];
    }
    if (fraction != null && fraction !== samples.at(-1)?.f) samples.push({ t: now, f: fraction });
    while (samples.length > 2 && now - samples[1].t > 30000) samples.shift();
    const first = samples[0], last = samples.at(-1);
    let left = null;
    if (first && last.f > first.f && last.t - first.t > 2500) {
      // Measured until now, so the estimate grows if it stalls.
      const perMs = (last.f - first.f) / (now - first.t);
      left = (1 - last.f) / perMs / 1000;
    }
    eta.hidden = left == null;
    if (left != null) eta.textContent = "faltan " + fmtLeft(left);
  }

  function fmtLeft(s) {
    if (s < 10) return "unos segundos";
    if (s < 60) return `~${Math.round(s / 5) * 5} s`;
    const m = Math.round(s / 60);
    if (m < 60) return `~${m} min`;
    return `~${Math.floor(m / 60)} h` + (m % 60 ? ` ${m % 60} min` : "");
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
