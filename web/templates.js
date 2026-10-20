"use strict";

// Reel templates. Each one is a recipe:
// - base: its starting configuration (duration, shapes, animation and
//   phrases), loaded into the controls when it's chosen;
// - aspect: the region of the source the shapes are computed on;
// - make(src): the content as a function of time, so the preview and the
//   export draw exactly the same frames;
// - phrases: texts that come and go along the reel, timed as fractions of
//   the duration so any duration works.
const Templates = (() => {
  const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x));
  const smooth = (x) => { x = clamp(x, 0, 1); return x * x * (3 - 2 * x); };

  // Areas the apps cover with their own buttons and captions (TikTok, Reels,
  // Shorts), as fractions of the frame: texts stay out of them.
  const SAFE = { top: 0.13, bottom: 0.2, right: 0.12, rightFrom: 0.45 };

  const BUILD = { order: "original", curve: "outCubic", effect: "fade", effectTime: 0.6 };

  // A phrase: { role, text, from, to, pos, style, extend? }. from/to are
  // fractions of the reel; from > to wraps around the loop point. extend:
  // with no other phrase shown after it, it stays until the end.
  const title = (from, to, extra) => ({ role: "title", text: "", from, to, pos: "top", style: "title", ...extra });
  const closing = (from, to) => ({ role: "closing", text: "", from, to, pos: "lower", style: "subtitle" });

  const LIST = {
    reveal: {
      name: "Revelado", kinds: ["image"], aspect: 9 / 16,
      desc: "Las formas arman la imagen y después aparece la foto real.",
      base: { duration: 15, shapes: 150, mode: 1, build: BUILD, phrases: [title(0.01, 0.68, { extend: true }), closing(0.74, 1)] },
      make: makeReveal,
    },
    loop: {
      name: "Loop hipnótico", kinds: ["image", "video"], aspect: 9 / 16,
      desc: "Se simplifica y se vuelve a armar; el final empalma con el principio.",
      // The title around the loop point, the closing in the middle: the cut stays invisible.
      base: { duration: 10, shapes: 150, mode: 1, build: BUILD, phrases: [title(0.82, 0.42, { extend: true }), closing(0.42, 0.82)] },
      make: makeLoop,
    },
    shapes: {
      name: "Video en formas", kinds: ["video"], aspect: 9 / 16,
      desc: "El video convertido en formas, con un fundido al original al final.",
      base: { duration: 15, shapes: 150, mode: 1, build: BUILD, phrases: [title(0.01, 0.68, { extend: true }), closing(0.74, 1)] },
      make: makeShapesVideo,
    },
  };
  const auto = (kind) => (kind === "video" ? "shapes" : "reveal");

  const POSITIONS = { top: "Arriba", middle: "Centro", lower: "Abajo" };
  const STYLES = { title: "Título", subtitle: "Subtítulo", question: "Pregunta", label: "Etiqueta" };
  const SHAPE_NAMES = { 0: "formas", 1: "triángulos", 2: "rectángulos", 3: "elipses", 4: "círculos", 5: "rectángulos", 6: "curvas", 7: "elipses", 8: "polígonos" };

  // How visible something shown from a to b is at t, fading over f seconds.
  // Edges at the start or the end of the reel don't fade.
  function windowAlpha(t, a, b, D, f = 0.45) {
    const fin = a <= 0 ? (t >= 0 ? 1 : 0) : smooth((t - a) / f);
    const fout = b >= D ? (t <= D ? 1 : 0) : smooth((b - t) / f);
    return Math.min(fin, fout);
  }

  // Draw paint() zoomed by s around the center (through a scratch canvas,
  // since the shape renderers set their own transform).
  function zoomed(ctx, w, h, s, scratches, paint) {
    const key = w + "x" + h; // one per output size (preview, cards, export)
    let scratch = scratches.get(key);
    if (!scratch) {
      scratch = document.createElement("canvas");
      scratch.width = w;
      scratch.height = h;
      scratches.set(key, scratch);
    }
    paint(scratch.getContext("2d"), w, h);
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.drawImage(scratch, (w - w * s) / 2, (h - h * s) / 2, w * s, h * s);
  }

  function overlay(ctx, a, paint) {
    if (a <= 0) return;
    ctx.save();
    ctx.globalAlpha = a;
    paint();
    ctx.restore();
  }

  // ---------- contents ----------

  // src: { kind, image (photo shapes) | anim (video keyframes), duration (photo),
  //        paintOriginal(ctx, w, h),
  //        build: { order, curve, effect, effectTime } (photos, as in the
  //        editor's recording), interp: "smooth" | "linear" | "step" (videos) }
  // Returns { id, duration, paint(ctx, t, w, h), videoTime(t), originalFrom }:
  // paint fills w x h with the content (no texts), videoTime says which
  // second of the portion the original video shows (null = none), and
  // originalFrom is when the original starts being visible.
  function make(id, src) {
    const b = { ...BUILD, ...src.build };
    return { id, ...LIST[id].make(src, b, src.interp || "smooth") };
  }

  function makeReveal(src, b) {
    const D = src.duration;
    const B = 0.5 * D, F = 0.62 * D, X = clamp(0.1 * D, 0.6, 2.5);
    const plan = Build.plan(src.image, {
      direction: "build", order: b.order, curve: b.curve, effect: b.effect,
      effectTime: Math.min(b.effectTime, B * 0.4), duration: B, holdStart: 0.2, holdEnd: D,
    });
    return {
      duration: D, videoTime: () => null, originalFrom: F,
      paint(ctx, t, w, h) {
        plan.render(ctx, t, w, h);
        overlay(ctx, smooth((t - F) / X), () => src.paintOriginal(ctx, w, h));
      },
    };
  }

  function makeShapesVideo(src, b, interp) {
    const D = src.anim.duration;
    const X = clamp(0.2 * D, 0.5, 2);
    return {
      duration: D, videoTime: (t) => t, originalFrom: D - X,
      paint(ctx, t, w, h) {
        Motion.render(ctx, src.anim, t, interp, w, h);
        overlay(ctx, smooth((t - (D - X)) / X), () => src.paintOriginal(ctx, w, h));
      },
    };
  }

  // Full detail at the start and the end, simplest (and closest) in the
  // middle, following a cosine so the speed is continuous across the cut.
  function makeLoop(src, b, interp) {
    const scratch = new Map();
    let D, frame;
    if (src.kind === "image") {
      D = src.duration;
      const plan = Build.plan(src.image, {
        // Build time runs 0..1 here: the effect lasts its share of half a loop.
        direction: "build", order: b.order, curve: "linear", effect: b.effect,
        effectTime: clamp(b.effectTime / (D / 2), 0.02, 0.5), duration: 1, holdStart: 0, holdEnd: 0,
      });
      const MIN = 0.06; // part of the shapes left at the simplest point
      frame = (ctx, t, w, h, c) => plan.render(ctx, MIN + (1 - MIN) * c, w, h);
    } else {
      // Video: forward and back again (boomerang).
      const L = src.anim.duration;
      D = 2 * L;
      frame = (ctx, t, w, h) => Motion.render(ctx, src.anim, t <= L ? t : 2 * L - t, interp, w, h);
    }
    return {
      duration: D, videoTime: () => null, originalFrom: Infinity,
      paint(ctx, t, w, h) {
        const c = 0.5 + 0.5 * Math.cos((2 * Math.PI * t) / D); // 1 at both ends
        zoomed(ctx, w, h, 1 + 0.08 * (1 - c), scratch, (g, gw, gh) => frame(g, t, gw, gh, c));
      },
    };
  }

  // ---------- phrases ----------

  // Base phrases of a template, keeping the texts already written (by role).
  function phrasesFor(id, current = []) {
    const texts = new Map(current.filter((p) => p.text.trim()).map((p) => [p.role, p.text]));
    return LIST[id].base.phrases.map((p) => ({ ...p, text: texts.get(p.role) ?? p.text }));
  }

  // Visibility of phrase p at t, in a reel of length D.
  function phraseAlpha(p, t, D, all) {
    const others = all.filter((q) => q !== p && q.text.trim());
    if (p.from > p.to) { // wraps around the loop point
      if (p.extend && !others.length) return 1;
      return Math.max(windowAlpha(t, p.from * D, D, D), windowAlpha(t, 0, p.to * D, D));
    }
    const to = p.extend && !others.some((q) => q.from > p.from && q.from <= q.to) ? 1 : p.to;
    return windowAlpha(t, p.from * D, to * D, D);
  }

  // {formas}, {tipo}... replaced by their values.
  const fill = (text, vars) => text.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m));

  const FONT = 'system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

  function wrap(ctx, text, maxWidth) {
    const lines = [];
    for (const para of text.split("\n")) {
      let line = "";
      for (const word of para.split(/\s+/).filter(Boolean)) {
        const next = line ? line + " " + word : word;
        if (line && ctx.measureText(next).width > maxWidth) {
          lines.push(line);
          line = word;
        } else line = next;
      }
      lines.push(line);
    }
    return lines.filter((l, i) => l || i < lines.length - 1);
  }

  // A centered block of text: anchor "top" puts its top at y, "center" its
  // middle. Shrinks the font until it fits in maxLines; pill draws a dark
  // rounded box behind it.
  function block(ctx, text, { x, y, maxWidth, size, weight, anchor, maxLines, alpha, rise, pill }) {
    if (!text.trim() || alpha <= 0.002) return;
    let lines;
    for (;;) {
      ctx.font = `${weight} ${Math.round(size)}px ${FONT}`;
      lines = wrap(ctx, text.trim(), maxWidth);
      if (lines.length <= maxLines || size < 12) break;
      size *= 0.9;
    }
    const lh = size * 1.18;
    let top = anchor === "top" ? y : y - (lines.length * lh) / 2;
    top += rise * (1 - alpha);
    ctx.globalAlpha = alpha;
    if (pill) {
      const w = Math.max(...lines.map((l) => ctx.measureText(l).width)) + size * 1.2;
      const h = lines.length * lh + size * 0.5;
      ctx.save();
      ctx.shadowColor = "transparent";
      ctx.fillStyle = "rgba(0, 0, 0, 0.5)";
      ctx.beginPath();
      ctx.roundRect(x - w / 2, top - size * 0.25, w, h, size * 0.4);
      ctx.fill();
      ctx.restore();
    }
    lines.forEach((l, i) => ctx.fillText(l, x, top + i * lh));
  }

  const STYLE_SPECS = {
    title: { size: 0.042, weight: 800, maxLines: 3 },
    subtitle: { size: 0.036, weight: 800, maxLines: 4 },
    question: { size: 0.05, weight: 900, maxLines: 3 },
    label: { size: 0.026, weight: 700, maxLines: 2, pill: true },
  };

  // Phrases (with their timing) and the brand, over a W x H frame at time t.
  function drawTexts(ctx, W, H, t, D, phrases, vars, brand) {
    ctx.save();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.textBaseline = "top";
    ctx.textAlign = "center";
    ctx.fillStyle = "#fff";
    ctx.shadowColor = "rgba(0, 0, 0, 0.7)";
    ctx.shadowBlur = H * 0.012;
    ctx.shadowOffsetY = H * 0.003;
    // Below the top bar the buttons column takes the right side: center the
    // text in the width that keeps clear of it on both sides.
    const narrow = W * (1 - 2 * (SAFE.right + 0.02));
    const places = {
      top: { y: H * (SAFE.top + 0.035), anchor: "top", maxWidth: W * 0.84 },
      middle: { y: H * 0.45, anchor: "center", maxWidth: narrow },
      lower: { y: H * 0.66, anchor: "center", maxWidth: narrow },
    };
    for (const p of phrases) {
      if (!p.text.trim()) continue;
      const s = STYLE_SPECS[p.style] ?? STYLE_SPECS.subtitle;
      block(ctx, fill(p.text, vars), {
        x: W / 2, ...(places[p.pos] ?? places.lower), size: H * s.size, weight: s.weight, maxLines: s.maxLines,
        pill: s.pill, alpha: phraseAlpha(p, t, D, phrases), rise: H * 0.015,
      });
    }
    if (brand?.trim()) {
      ctx.textAlign = "left";
      ctx.textBaseline = "bottom";
      ctx.font = `600 ${Math.round(H * 0.021)}px ${FONT}`;
      ctx.globalAlpha = 0.9;
      ctx.fillText(brand.trim(), W * 0.06, H * (1 - SAFE.bottom - 0.015), W * (1 - SAFE.right - 0.1));
    }
    ctx.restore();
  }

  return { LIST, SAFE, POSITIONS, STYLES, SHAPE_NAMES, auto, make, phrasesFor, phraseAlpha, drawTexts };
})();
