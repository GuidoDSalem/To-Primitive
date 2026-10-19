"use strict";

// Reel templates: how the shapes, the original and the texts play out over
// the reel. Everything is a function of time, so the preview and the export
// draw exactly the same frames. Timings scale with the duration, so any
// duration works.
const Templates = (() => {
  const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x));
  const smooth = (x) => { x = clamp(x, 0, 1); return x * x * (3 - 2 * x); };

  // Areas the apps cover with their own buttons and captions (TikTok, Reels,
  // Shorts), as fractions of the frame: texts stay out of them.
  const SAFE = { top: 0.13, bottom: 0.2, right: 0.12, rightFrom: 0.45 };

  const LIST = {
    reveal: { name: "Revelado", kinds: ["image"], desc: "Las formas arman la imagen y después aparece la foto real." },
    loop: { name: "Loop hipnótico", kinds: ["image", "video"], desc: "Se simplifica y se vuelve a armar; el final empalma con el principio." },
    shapes: { name: "Video en formas", kinds: ["video"], desc: "El video convertido en formas, con un fundido al original al final." },
  };
  const auto = (kind) => (kind === "video" ? "shapes" : "reveal");

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

  // src: { kind, image (photo shapes) | anim (video keyframes), duration (photo),
  //        hasClosing, paintOriginal(ctx, w, h) }
  // Returns { id, duration, paint(ctx, t, w, h), videoTime(t), textAlpha(t) }:
  // paint fills w x h with the content (no texts), videoTime says which
  // second of the portion the original video must show (null = not needed),
  // textAlpha gives the visibility of the title and the closing.
  function make(id, src) {
    const scratch = new Map();
    if (id === "reveal") {
      const D = src.duration;
      const B = 0.5 * D, F = 0.62 * D, X = clamp(0.1 * D, 0.6, 2.5);
      const plan = Build.plan(src.image, {
        direction: "build", order: "original", curve: "outCubic", effect: "fade",
        effectTime: Math.min(0.6, B * 0.2), duration: B, holdStart: 0.2, holdEnd: D,
      });
      return {
        id, duration: D, videoTime: () => null,
        paint(ctx, t, w, h) {
          plan.render(ctx, t, w, h);
          overlay(ctx, smooth((t - F) / X), () => src.paintOriginal(ctx, w, h));
        },
        textAlpha: (t) => src.hasClosing
          ? { title: windowAlpha(t, 0.15, 0.68 * D, D), closing: windowAlpha(t, 0.74 * D, D, D) }
          : { title: windowAlpha(t, 0.15, D, D), closing: 0 },
      };
    }

    if (id === "shapes") {
      const D = src.anim.duration;
      const X = clamp(0.2 * D, 0.5, 2);
      return {
        id, duration: D, videoTime: (t) => t,
        paint(ctx, t, w, h) {
          Motion.render(ctx, src.anim, t, "smooth", w, h);
          overlay(ctx, smooth((t - (D - X)) / X), () => src.paintOriginal(ctx, w, h));
        },
        textAlpha: (t) => src.hasClosing
          ? { title: windowAlpha(t, 0.15, 0.68 * D, D), closing: windowAlpha(t, 0.74 * D, D, D) }
          : { title: windowAlpha(t, 0.15, D, D), closing: 0 },
      };
    }

    if (id === "loop") {
      // Full detail at the start and the end, simplest (and closest) in the
      // middle, following a cosine so the speed is continuous across the cut.
      let D, frame;
      if (src.kind === "image") {
        D = src.duration;
        const plan = Build.plan(src.image, {
          direction: "build", order: "original", curve: "linear", effect: "fade",
          effectTime: 0.1, duration: 1, holdStart: 0, holdEnd: 0,
        });
        const MIN = 0.06; // part of the shapes left at the simplest point
        frame = (ctx, t, w, h, c) => plan.render(ctx, MIN + (1 - MIN) * c, w, h);
      } else {
        // Video: forward and back again (boomerang).
        const L = src.anim.duration;
        D = 2 * L;
        frame = (ctx, t, w, h) => Motion.render(ctx, src.anim, t <= L ? t : 2 * L - t, "smooth", w, h);
      }
      return {
        id, duration: D, videoTime: () => null,
        paint(ctx, t, w, h) {
          const c = 0.5 + 0.5 * Math.cos((2 * Math.PI * t) / D); // 1 at both ends
          zoomed(ctx, w, h, 1 + 0.08 * (1 - c), scratch, (g, gw, gh) => frame(g, t, gw, gh, c));
        },
        // The closing shows in the middle; the title around the loop point,
        // so the cut stays invisible.
        textAlpha(t) {
          const closing = src.hasClosing ? windowAlpha(t, 0.42 * D, 0.82 * D, D) : 0;
          return { title: 1 - closing, closing };
        },
      };
    }
    throw new Error("template desconocido: " + id);
  }

  // ---------- texts ----------

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
  // middle. Shrinks the font until it fits in maxLines.
  function block(ctx, text, { x, y, maxWidth, size, weight, anchor, maxLines, alpha, rise }) {
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
    lines.forEach((l, i) => ctx.fillText(l, x, top + i * lh));
  }

  // texts: { title, closing, brand }; alpha: { title, closing }.
  function draw(ctx, W, H, texts, alpha) {
    ctx.save();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.textBaseline = "top";
    ctx.textAlign = "center";
    ctx.fillStyle = "#fff";
    ctx.shadowColor = "rgba(0, 0, 0, 0.7)";
    ctx.shadowBlur = H * 0.012;
    ctx.shadowOffsetY = H * 0.003;
    const rise = H * 0.015;
    block(ctx, texts.title || "", {
      x: W / 2, y: H * (SAFE.top + 0.035), maxWidth: W * 0.84, size: H * 0.042, weight: 800,
      anchor: "top", maxLines: 3, alpha: alpha.title, rise,
    });
    // The closing sits next to the buttons column: center it in the width
    // that keeps clear of it on both sides.
    block(ctx, texts.closing || "", {
      x: W / 2, y: H * 0.66, maxWidth: W * (1 - 2 * (SAFE.right + 0.02)), size: H * 0.036, weight: 800,
      anchor: "center", maxLines: 4, alpha: alpha.closing, rise,
    });
    if (texts.brand?.trim()) {
      ctx.textAlign = "left";
      ctx.textBaseline = "bottom";
      ctx.font = `600 ${Math.round(H * 0.021)}px ${FONT}`;
      ctx.globalAlpha = 0.9;
      ctx.fillText(texts.brand.trim(), W * 0.06, H * (1 - SAFE.bottom - 0.015), W * (1 - SAFE.right - 0.1));
    }
    ctx.restore();
  }

  return { LIST, SAFE, auto, make, draw };
})();
