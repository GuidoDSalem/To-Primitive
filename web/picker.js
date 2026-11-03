"use strict";

// Visual option pickers: replaces a <select> with a grid of cards, each one
// a small looping animation of what that option does. The <select> stays in
// the DOM (hidden) as the source of truth, so the rest of the app keeps
// reading its value and listening to its "input" events.
const Picker = (() => {
  const cards = [];
  let raf = 0, last = 0;
  const FPS = 24;

  // makeSource(value) -> { duration, render(ctx, t, w, h), aspect? } or null.
  function attach(select, makeSource) {
    select.hidden = true;
    const grid = document.createElement("div");
    grid.className = "picks";
    select.after(grid);
    const mine = [];
    for (const opt of select.options) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "pick";
      b.title = opt.textContent;
      const canvas = document.createElement("canvas");
      canvas.width = 180;
      canvas.height = 120;
      const label = document.createElement("span");
      label.textContent = opt.textContent;
      b.append(canvas, label);
      b.addEventListener("click", () => {
        if (select.value === opt.value) return;
        select.value = opt.value;
        select.dispatchEvent(new Event("input", { bubbles: true }));
      });
      grid.append(b);
      const card = { value: opt.value, button: b, canvas, ctx: canvas.getContext("2d"), src: undefined, visible: false, makeSource, select };
      mine.push(card);
      cards.push(card);
    }
    const sync = () => { for (const c of mine) c.button.classList.toggle("on", c.value === select.value); };
    select.addEventListener("input", sync);
    select.addEventListener("change", sync);
    sync();
    observer.observe(grid);
    grid._cards = mine;
    return { sync };
  }

  const observer = new IntersectionObserver((entries) => {
    for (const e of entries) for (const c of e.target._cards) c.visible = e.isIntersecting;
    schedule();
  });

  // Sources depend on the image and the other options: rebuild lazily.
  function refresh() {
    clearTimeout(soon);
    for (const c of cards) c.src = undefined;
    schedule();
  }
  // Same, coalescing bursts (e.g. while shapes keep arriving).
  let soon = 0;
  function refreshSoon() {
    clearTimeout(soon);
    soon = setTimeout(refresh, 400);
  }

  // Draw the source letterboxed into the card, keeping the image's aspect.
  const scratch = document.createElement("canvas");
  function draw(c, now) {
    if (c.src === undefined) {
      try { c.src = c.makeSource(c.value); } catch (e) { console.error(e); c.src = null; }
    }
    const { ctx, canvas } = c;
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.clearRect(0, 0, canvas.width, canvas.height);
    if (!c.src) return;
    const t = c.src.duration > 0 ? ((now / 1000) % c.src.duration) : 0;
    if (!c.src.width) { // draws itself, whole card
      c.src.render(ctx, t, canvas.width, canvas.height);
      return;
    }
    const scale = Math.min(canvas.width / c.src.width, canvas.height / c.src.height);
    const w = Math.max(1, Math.round(c.src.width * scale)), h = Math.max(1, Math.round(c.src.height * scale));
    if (scratch.width !== w || scratch.height !== h) { scratch.width = w; scratch.height = h; }
    const sx = scratch.getContext("2d");
    c.src.render(sx, t, w, h);
    ctx.drawImage(scratch, (canvas.width - w) / 2, (canvas.height - h) / 2);
  }

  function frame(now) {
    raf = 0;
    if (document.hidden) return;
    if (now - last >= 1000 / FPS) {
      last = now;
      for (const c of cards) {
        if (c.visible && !c.button.closest("[hidden]")) draw(c, now);
      }
    }
    if (cards.some((c) => c.visible)) raf = requestAnimationFrame(frame);
  }

  function schedule() { if (!raf) raf = requestAnimationFrame(frame); }
  document.addEventListener("visibilitychange", schedule);

  // Preview of an easing curve: its plot, a dot running along it, and a bar
  // showing how much of the image is built.
  function curve(ease) {
    const css = getComputedStyle(document.documentElement);
    return {
      duration: 2.6,
      render(ctx, t, w, h) {
        const p = Math.min(1, t / 2);
        const pad = 12, gw = w - 2 * pad, gh = h - 2 * pad - 14;
        ctx.strokeStyle = css.getPropertyValue("--line");
        ctx.lineWidth = 2;
        ctx.strokeRect(pad, pad, gw, gh);
        ctx.strokeStyle = css.getPropertyValue("--accent");
        ctx.lineWidth = 4;
        ctx.beginPath();
        for (let k = 0; k <= 60; k++) {
          const x = pad + (k / 60) * gw, y = pad + gh - ease(k / 60) * gh;
          k ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
        }
        ctx.stroke();
        ctx.fillStyle = css.getPropertyValue("--text");
        ctx.beginPath();
        ctx.arc(pad + p * gw, pad + gh - ease(p) * gh, 7, 0, 2 * Math.PI);
        ctx.fill();
        ctx.fillStyle = css.getPropertyValue("--line");
        ctx.fillRect(pad, h - pad - 6, gw, 6);
        ctx.fillStyle = css.getPropertyValue("--accent");
        ctx.fillRect(pad, h - pad - 6, gw * ease(p), 6);
      },
    };
  }

  // A "Curva / Resultado" toggle (chips with data-value "curve" / "result")
  // that switches what the curve cards show; remembered per page.
  function curveToggle(chips, def) {
    const key = "curveView:" + location.pathname;
    let value = def;
    try { value = localStorage.getItem(key) || def; } catch { /* storage unavailable */ }
    const sync = () => { for (const b of chips.children) b.classList.toggle("on", b.dataset.value === value); };
    chips.addEventListener("click", (e) => {
      const b = e.target.closest("button");
      if (!b || b.dataset.value === value) return;
      value = b.dataset.value;
      try { localStorage.setItem(key, value); } catch { /* storage unavailable */ }
      sync();
      refresh();
    });
    sync();
    return () => value;
  }

  return { attach, refresh, refreshSoon, curve, curveToggle };
})();
