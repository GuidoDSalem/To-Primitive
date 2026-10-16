"use strict";

// "Recording" of an image being built shape by shape: a timeline that decides
// when each shape appears (speed curve, order, direction) and how (effect).
// Shapes use the same flat format as the video keyframes (see encode.go).
const Build = (() => {
  const H = 6;
  const clamp01 = (x) => Math.min(1, Math.max(0, x));

  // Speed curves ("easing"): map time progress 0..1 to build progress 0..1.
  const inC = (p) => p * p * p, outC = (p) => 1 - (1 - p) ** 3;
  const CURVES = {
    linear:     { label: "Constante", f: (p) => p },
    inQuad:     { label: "Acelera suave", f: (p) => p * p },
    inCubic:    { label: "Acelera", f: inC },
    inExpo:     { label: "Acelera fuerte (exponencial)", f: (p) => (p === 0 ? 0 : 2 ** (10 * p - 10)) },
    outQuad:    { label: "Frena suave", f: (p) => 1 - (1 - p) ** 2 },
    outCubic:   { label: "Frena", f: outC },
    outExpo:    { label: "Frena fuerte (exponencial)", f: (p) => (p === 1 ? 1 : 1 - 2 ** (-10 * p)) },
    inOutCubic: { label: "Lento → rápido → lento (campana)", f: (p) => (p < 0.5 ? 4 * p ** 3 : 1 - (-2 * p + 2) ** 3 / 2) },
    inOutSine:  { label: "Lento → rápido → lento (suave)", f: (p) => -(Math.cos(Math.PI * p) - 1) / 2 },
    outInCubic: { label: "Rápido → lento → rápido", f: (p) => (p < 0.5 ? outC(2 * p) / 2 : (inC(2 * p - 1) + 1) / 2) },
    impact:     { label: "Por impacto visual", f: null }, // built from the scores
  };

  const ORDERS = {
    original: "Como las encontró el algoritmo (grandes primero)",
    reverse: "Al revés (detalles primero)",
    random: "Aleatorio",
    size: "Por tamaño (más grandes primero)",
    top: "De arriba hacia abajo",
    left: "De izquierda a derecha",
    center: "Del centro hacia afuera",
    outside: "De afuera hacia el centro",
  };

  const EFFECTS = {
    none: "Aparecer de golpe",
    fade: "Fundido",
    grow: "Crecer",
    pop: "Rebote (crecer y pasarse)",
    drop: "Caer desde arriba",
  };

  const DIRECTIONS = {
    build: "Construir",
    unbuild: "Deconstruir",
    pingpong: "Construir y deconstruir",
  };

  // Center and rough area of a shape, for ordering and effects.
  function geometry(s) {
    let cx = 0, cy = 0, area = 0;
    switch (s[0]) {
      case 0: { // polygon: centroid of vertices, shoelace area
        const n = (s.length - H) / 2;
        for (let j = 0; j < n; j++) {
          const x1 = s[H + 2 * j], y1 = s[H + 2 * j + 1];
          const x2 = s[H + 2 * ((j + 1) % n)], y2 = s[H + 2 * ((j + 1) % n) + 1];
          cx += x1 / n; cy += y1 / n; area += (x1 * y2 - x2 * y1) / 2;
        }
        area = Math.abs(area);
        break;
      }
      case 1: cx = s[H]; cy = s[H + 1]; area = s[H + 2] * s[H + 3]; break;
      case 2: cx = s[H]; cy = s[H + 1]; area = Math.PI * s[H + 2] * s[H + 3]; break;
      case 3: cx = (s[H] + s[H + 2] + s[H + 4]) / 3; cy = (s[H + 1] + s[H + 3] + s[H + 5]) / 3;
        area = Math.hypot(s[H + 4] - s[H], s[H + 5] - s[H + 1]) * s[H + 6]; break;
    }
    return { cx, cy, area };
  }

  // Deterministic shuffle so the preview and the export match.
  function shuffled(n) {
    const idx = [...Array(n).keys()];
    let seed = 12345;
    const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
    for (let i = n - 1; i > 0; i--) {
      const j = Math.floor(rnd() * (i + 1));
      [idx[i], idx[j]] = [idx[j], idx[i]];
    }
    return idx;
  }

  // Appearance rank of each shape (0 = first). Drawing order never changes,
  // so the final image is always the same.
  function ranks(shapes, order, width, height) {
    const n = shapes.length;
    const g = shapes.map(geometry);
    const key = {
      original: (i) => i,
      reverse: (i) => -i,
      size: (i) => -g[i].area,
      top: (i) => g[i].cy,
      left: (i) => g[i].cx,
      center: (i) => Math.hypot(g[i].cx - width / 2, g[i].cy - height / 2),
      outside: (i) => -Math.hypot(g[i].cx - width / 2, g[i].cy - height / 2),
    }[order];
    const seq = order === "random" ? shuffled(n) : [...Array(n).keys()].sort((a, b) => key(a) - key(b) || a - b);
    const rank = new Array(n);
    seq.forEach((i, r) => { rank[i] = r; });
    return { rank, geometry: g };
  }

  // Time progress -> build progress for "impact": equal visual improvement per
  // unit of time, so shapes that change the image a lot stay on screen longer
  // and details fly by. gains[r] is how much the r-th shape to appear improved
  // the image (in appearance order).
  function impactCurve(gains) {
    const n = gains.length;
    const total = gains.reduce((a, g) => a + g, 0);
    if (!(total > 0)) return CURVES.linear.f;
    const cum = [0]; // cum[k]: share of the improvement after k shapes
    for (const g of gains) cum.push(cum.at(-1) + g / total);
    return (p) => {
      // cum is non-decreasing: find k with cum[k] <= p < cum[k+1] and interpolate.
      let lo = 0, hi = n;
      while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (cum[mid] <= p) lo = mid; else hi = mid - 1; }
      if (lo >= n) return 1;
      const span = cum[lo + 1] - cum[lo];
      return (lo + (span > 0 ? (p - cum[lo]) / span : 0)) / n;
    };
  }

  // opts: { curve, order, effect, effectTime, direction, duration, holdStart, holdEnd }
  // Returns a source the player and the exporter can use.
  // When each shape starts appearing, and how timeline time maps to "build
  // time" (0 = nothing, main = complete), given how long each shape's own
  // transition lasts. Shared by the 2D recording and the 3D scene.
  function schedule(img, opts, transition) {
    const shapes = img.shapes, n = shapes.length;
    const { rank, geometry } = ranks(shapes, opts.order, img.width, img.height);
    let ease = CURVES[opts.curve].f;
    if (opts.curve === "impact") {
      const gains = new Array(n);
      for (let i = 0; i < n; i++) gains[rank[i]] = Math.max(0, (i ? img.scores[i - 1] : img.baseScore) - img.scores[i]);
      ease = impactCurve(gains);
    }
    const span = Math.max(0.001, opts.duration - transition); // shapes start within this
    // Start time of each rank: invert the curve numerically (it's monotonic).
    const starts = new Float64Array(n);
    const steps = 4000;
    let r = 0;
    for (let k = 0; k <= steps && r < n; k++) {
      const done = ease(k / steps) * n; // shapes started by now
      while (r < n && done > r) starts[r++] = (k / steps) * span;
    }
    while (r < n) starts[r++] = span;

    const pingpong = opts.direction === "pingpong";
    const main = opts.duration;
    const total = opts.holdStart + main + (pingpong ? opts.holdEnd + main : 0) + opts.holdEnd;

    function buildTime(t) {
      t -= opts.holdStart;
      if (t <= 0) return opts.direction === "unbuild" ? main : 0;
      if (opts.direction === "unbuild") return Math.max(0, main - t);
      if (t <= main) return t;
      if (!pingpong) return main;
      t -= main + opts.holdEnd;
      return t <= 0 ? main : Math.max(0, main - t);
    }

    // Start time of shape i (in drawing order).
    const startOf = (i) => starts[rank[i]];
    return { rank, geometry, startOf, ease, main, total, buildTime };
  }

  function plan(img, opts) {
    const shapes = img.shapes, n = shapes.length;
    const effectTime = opts.effect === "none" ? 0 : Math.min(opts.effectTime, opts.duration * 0.9);
    const { geometry: geo, startOf, ease, total, buildTime } = schedule(img, opts, effectTime);

    function render(ctx, t, w, h) {
      ctx.setTransform(1, 0, 0, 1, 0, 0);
      ctx.fillStyle = img.background;
      ctx.fillRect(0, 0, w, h);
      const sx = w / img.width, sy = h / img.height;
      const bt = buildTime(t);
      for (let i = 0; i < n; i++) {
        const local = effectTime > 0 ? clamp01((bt - startOf(i)) / effectTime) : (bt >= startOf(i) && bt > 0 ? 1 : 0);
        if (local <= 0) continue;
        let factor = 1, scale = 1, dy = 0;
        switch (opts.effect) {
          case "fade": factor = local; break;
          case "grow": scale = 1 - (1 - local) ** 3; break;
          case "pop": { // easeOutBack
            const c1 = 1.70158, c3 = c1 + 1;
            scale = 1 + c3 * (local - 1) ** 3 + c1 * (local - 1) ** 2;
            break;
          }
          case "drop": factor = local; dy = -(1 - (1 - (1 - local) ** 3)) * img.height * 0.15; break;
        }
        const { cx, cy } = geo[i];
        ctx.setTransform(sx, 0, 0, sy, 0.5 * sx, 0.5 * sy);
        if (scale !== 1 || dy) {
          ctx.translate(cx, cy + dy);
          ctx.scale(scale, scale);
          ctx.translate(-cx, -cy);
        }
        Motion.drawShape(ctx, shapes[i], factor);
      }
    }

    return { width: img.width, height: img.height, duration: total, render, ease };
  }

  return { CURVES, ORDERS, EFFECTS, DIRECTIONS, plan, schedule, geometry, shuffled };
})();
