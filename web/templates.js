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
  const closing = (from, to, text = "") => ({ role: "closing", text, from, to, pos: "lower", style: "subtitle" });
  const phrase = (role, text, from, to, pos, style, extra) => ({ role, text, from, to, pos, style, ...extra });

  const LIST = {
    reveal: {
      name: "Revelado", kinds: ["image"], aspect: 9 / 16,
      desc: "Las formas arman la imagen y después aparece la foto real.",
      base: { duration: 15, shapes: 150, mode: 1, build: BUILD, ending: 5.5, phrases: [title(0.01, 0.68, { extend: true }), closing(0.74, 1)] },
      make: makeReveal, ending: true, // ends showing the original (how long: src.ending)
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
      base: { duration: 15, shapes: 150, mode: 1, build: BUILD, ending: 2, phrases: [title(0.01, 0.68, { extend: true }), closing(0.74, 1)] },
      make: makeShapesVideo, ending: true, // ends showing the original (how long: src.ending)
    },
    levels: {
      name: "¿En qué nivel?", kinds: ["image", "video"], aspect: 9 / 16,
      desc: "Se completa de a niveles (10, 25, 50… formas) y al final aparece el original: ¿en qué nivel la adivinaste?",
      base: {
        duration: 15, shapes: 150, mode: 1, build: BUILD, ending: 3,
        phrases: [
          phrase("question", "¿En qué nivel la adivinaste?", 0.01, 0.8, "top", "question"),
          phrase("level", "Nivel {nivel} · {formas} formas", 0, 0.8, "foot", "label"),
          closing(0.84, 1, "Comentá en qué nivel la adivinaste 👇"),
        ],
      },
      make: makeLevels, ending: true, // ends showing the original (how long: src.ending)
    },
    compare: {
      name: "Comparativa", kinds: ["image", "video"], aspect: 9 / 8, stack: 2,
      desc: "Arriba el original, abajo las formas armándose, separados por una línea.",
      base: {
        duration: 12, shapes: 200, mode: 1, build: BUILD,
        phrases: [
          phrase("topLabel", "Original", 0, 1, "top", "label"),
          phrase("bottomLabel", "{formas} {tipo}", 0, 1, "half", "label"),
          closing(0.74, 1),
        ],
      },
      make: makeCompare,
    },
    curtain: {
      name: "Cortina", kinds: ["image", "video"], aspect: 9 / 16,
      desc: "Una línea va y viene: de un lado el original, del otro las formas.",
      base: {
        duration: 12, shapes: 200, mode: 1, build: BUILD,
        phrases: [phrase("question", "¿Se nota la diferencia?", 0.01, 0.7, "top", "question", { extend: true }), closing(0.74, 1)],
      },
      make: makeCurtain,
    },
    counter: {
      name: "Contador", kinds: ["image"], aspect: 9 / 16,
      desc: "Las formas se suman con un contador grande; al final, la foto.",
      base: {
        duration: 12, shapes: 300, mode: 1, build: { ...BUILD, curve: "inOutCubic" }, ending: 2,
        phrases: [phrase("counter", "{n} {tipo}", 0, 1, "top", "counter"), closing(0.88, 1)],
      },
      make: makeCounter, ending: true, // ends showing the original (how long: src.ending)
    },
    mosaic: {
      name: "Mosaico de niveles", kinds: ["image", "video"], aspect: 9 / 16,
      desc: "Cuatro cuadros con cada vez más formas (10, 50, 100… todas) y al final el original.",
      base: {
        duration: 12, shapes: 500, mode: 1, build: BUILD, ending: 2,
        phrases: [
          phrase("question", "¿Cuántas formas hacen falta?", 0.01, 0.8, "top", "question"),
          closing(0.86, 1, "¿Con cuál la reconociste? 👇"),
        ],
      },
      make: makeMosaic, ending: true, // ends showing the original (how long: src.ending)
    },
    prefer: {
      name: "¿Cuál preferís?", kinds: ["image"], aspect: 9 / 16,
      // Each kind of shape is a computation of its own.
      variants: [1, 4, 2, 8],
      desc: "La misma foto con cuatro tipos de forma (triángulos, círculos, rectángulos, polígonos) armándose a la vez.",
      base: {
        duration: 12, shapes: 150, mode: 1, build: BUILD,
        phrases: [
          phrase("question", "¿Cuál te gusta más?", 0.01, 0.7, "top", "question", { extend: true }),
          closing(0.72, 1, "Comentá 1, 2, 3 o 4 👇"),
        ],
      },
      make: makePrefer,
    },
    countCurtain: {
      name: "Contador y cortina", kinds: ["image", "video"], aspect: 9 / 16,
      desc: "Las formas se suman con un contador; cuando está completa, una cortina la compara con el original.",
      base: {
        duration: 14, shapes: 300, mode: 1, build: { ...BUILD, curve: "inOutCubic" },
        phrases: [
          phrase("counter", "{n} {tipo}", 0, 0.48, "top", "counter"),
          phrase("question", "¿Se nota la diferencia?", 0.52, 0.86, "top", "question"),
          closing(0.88, 1),
        ],
      },
      make: makeCountCurtain,
    },
    countVideo: {
      name: "Contador y video", kinds: ["video"], aspect: 9 / 16,
      desc: "Un contador arma el primer cuadro con formas; cuando está completo, el video arranca con las formas en movimiento.",
      base: {
        duration: 15, shapes: 150, mode: 1, build: { ...BUILD, curve: "inOutCubic" }, ending: 2,
        phrases: [phrase("counter", "{n} {tipo}", 0, 0.3, "top", "counter"), title(0.33, 0.7, { extend: true }), closing(0.76, 1)],
      },
      make: makeCountVideo, ending: true, // ends showing the original (how long: src.ending)
    },
    cover: {
      name: "Tapado", kinds: ["image", "video"], aspect: 9 / 16,
      desc: "Empieza con el original y las formas lo van tapando hasta que solo quedan ellas.",
      base: { duration: 12, shapes: 200, mode: 1, build: BUILD, phrases: [title(0.01, 0.7, { extend: true }), closing(0.76, 1)] },
      make: makeCover,
    },
  };
  const auto = (kind) => (kind === "video" ? "shapes" : "reveal");

  const POSITIONS = { top: "Arriba", half: "Mitad (debajo de la línea)", middle: "Centro", lower: "Abajo", foot: "Al pie" };
  const STYLES = { title: "Título", subtitle: "Subtítulo", question: "Pregunta", label: "Etiqueta", counter: "Contador (grande)" };
  const SHAPE_NAMES = { 0: "formas", 1: "triángulos", 2: "rectángulos", 3: "elipses", 4: "círculos", 5: "rectángulos", 6: "curvas", 7: "elipses", 8: "polígonos" };
  // Words the templates draw themselves, per language (others fall back to English).
  const WORDS = {
    es: { shapes: "formas", names: SHAPE_NAMES },
    en: { shapes: "shapes", names: { 0: "shapes", 1: "triangles", 2: "rectangles", 3: "ellipses", 4: "circles", 5: "rectangles", 6: "curves", 7: "ellipses", 8: "polygons" } },
    pt: { shapes: "formas", names: { 0: "formas", 1: "triângulos", 2: "retângulos", 3: "elipses", 4: "círculos", 5: "retângulos", 6: "curvas", 7: "elipses", 8: "polígonos" } },
  };
  const words = (lang) => WORDS[lang] ?? WORDS.en;

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

  // Draw paint(g, w, h) into the rectangle x, y, w, h of ctx (through a
  // scratch canvas: the shape renderers set their own transform).
  function inRect(ctx, x, y, w, h, scratches, paint) {
    w = Math.max(1, Math.round(w));
    h = Math.max(1, Math.round(h));
    const key = w + "x" + h;
    let scratch = scratches.get(key);
    if (!scratch) {
      scratch = document.createElement("canvas");
      scratch.width = w;
      scratch.height = h;
      scratches.set(key, scratch);
    }
    paint(scratch.getContext("2d"), w, h);
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.drawImage(scratch, Math.round(x), Math.round(y));
  }

  // The first n shapes of a photo, the ones from `from` on faded by f.
  function drawShapes(ctx, img, w, h, n, from = n, f = 1) {
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.fillStyle = img.background;
    ctx.fillRect(0, 0, w, h);
    const sx = w / img.width, sy = h / img.height;
    ctx.setTransform(sx, 0, 0, sy, 0.5 * sx, 0.5 * sy);
    for (let i = 0; i < Math.min(n, img.shapes.length); i++) Motion.drawShape(ctx, img.shapes[i], i < from ? 1 : f);
    ctx.setTransform(1, 0, 0, 1, 0, 0);
  }

  const shapeCount = (src) => (src.kind === "image" ? src.image.shapes.length : Math.max(0, ...src.anim.keyframes.map((k) => k.length)));
  const lengthOf = (src) => (src.kind === "image" ? src.duration : src.anim.duration);

  function overlay(ctx, a, paint) {
    if (a <= 0) return;
    ctx.save();
    ctx.globalAlpha = a;
    paint();
    ctx.restore();
  }

  // ---------- contents ----------

  // src: { kind, image (photo shapes) | anim (video keyframes), duration (photo),
  //        paintOriginal(ctx, x, y, w, h): the original into that rectangle,
  //        build: { order, curve, effect, effectTime } (photos, as in the
  //        editor's recording), interp: "smooth" | "linear" | "step" (videos) }
  // Returns { id, duration, paint(ctx, t, w, h), videoTime(t), originalFrom,
  // vars?(t) }: paint fills w x h with the content (no texts), videoTime says
  // which second of the portion the original video shows (null = none),
  // originalFrom is when the original starts being visible, and vars gives
  // values for the phrases ({nivel}, {n}...).
  function make(id, src) {
    const b = { ...BUILD, ...src.build };
    return { id, ...LIST[id].make(src, b, src.interp || "smooth") };
  }

  // When the original starts appearing at the end and how long its fade
  // takes: the template's own moment, or src.ending seconds before the end
  // when it's set (null: the template's; 0: the original isn't shown).
  function finale(src, D, from, fade) {
    if (src.ending == null) return [from, fade];
    if (src.ending <= 0) return [D, 1e-3]; // never reached: the shapes until the end
    const e = clamp(src.ending, 0.3, 0.85 * D);
    return [D - e, Math.min(fade, e)];
  }

  function makeReveal(src, b) {
    const D = src.duration;
    const [F, X] = finale(src, D, 0.62 * D, clamp(0.1 * D, 0.6, 2.5));
    const B = Math.min(0.5 * D, 0.8 * F); // built before the original comes
    const plan = Build.plan(src.image, {
      direction: "build", order: b.order, curve: b.curve, effect: b.effect,
      effectTime: Math.min(b.effectTime, B * 0.4), duration: B, holdStart: 0.2, holdEnd: D,
    });
    return {
      duration: D, videoTime: () => null, originalFrom: F,
      paint(ctx, t, w, h) {
        plan.render(ctx, t, w, h);
        overlay(ctx, smooth((t - F) / X), () => src.paintOriginal(ctx, 0, 0, w, h));
      },
    };
  }

  function makeShapesVideo(src, b, interp) {
    const D = src.anim.duration;
    const fade = clamp(0.2 * D, 0.5, 2);
    const [F, X] = finale(src, D, D - fade, fade);
    return {
      duration: D, videoTime: (t) => t, originalFrom: F,
      paint(ctx, t, w, h) {
        Motion.render(ctx, src.anim, t, interp, w, h);
        overlay(ctx, smooth((t - F) / X), () => src.paintOriginal(ctx, 0, 0, w, h));
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

  // Levels: 10, 25, 50, 100... shapes (at most five), then all of them.
  function levelCounts(n) {
    const steps = [10, 25, 50, 100, 250, 500, 1000, 2500].filter((k) => k < n * 0.8);
    return [...steps.slice(-4), n];
  }

  // The image completes level by level (each one held), then the original.
  // Videos play the portion with each level's shapes in turn.
  function makeLevels(src, b, interp) {
    const D = lengthOf(src), counts = levelCounts(shapeCount(src)), L = counts.length;
    const [R, X] = finale(src, D, 0.8 * D, clamp(0.08 * D, 0.5, 1.5));
    const per = R / L;
    const level = (t) => clamp(Math.floor(t / per), 0, L - 1);
    return {
      duration: D, videoTime: src.kind === "video" ? (t) => t : () => null, originalFrom: R,
      vars: (t) => ({ nivel: level(t) + 1, niveles: L, formas: counts[level(t)] }),
      paint(ctx, t, w, h) {
        const k = level(t);
        if (src.kind === "image") {
          // The new shapes of each level fade in quickly.
          const u = smooth((t - k * per) / Math.min(0.5, per * 0.4));
          drawShapes(ctx, src.image, w, h, counts[k], k ? counts[k - 1] : 0, u);
        } else {
          Motion.render(ctx, src.anim, t, interp, w, h, counts[k]);
        }
        overlay(ctx, smooth((t - R) / X), () => src.paintOriginal(ctx, 0, 0, w, h));
      },
    };
  }

  // Original on top, shapes below (each a 9:8 panel), a line between them.
  function makeCompare(src, b, interp) {
    const D = lengthOf(src), scratch = new Map();
    const plan = src.kind === "image" && Build.plan(src.image, {
      direction: "build", order: b.order, curve: b.curve, effect: b.effect,
      effectTime: Math.min(b.effectTime, D * 0.2), duration: 0.65 * D, holdStart: 0.3, holdEnd: D,
    });
    return {
      duration: D, videoTime: src.kind === "video" ? (t) => t : () => null, originalFrom: 0,
      paint(ctx, t, w, h) {
        const hh = Math.round(h / 2);
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.fillStyle = "#000";
        ctx.fillRect(0, 0, w, h);
        src.paintOriginal(ctx, 0, 0, w, hh);
        inRect(ctx, 0, hh, w, h - hh, scratch, (g, gw, gh) => (plan ? plan.render(g, t, gw, gh) : Motion.render(g, src.anim, t, interp, gw, gh)));
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.fillStyle = "#fff";
        ctx.fillRect(0, hh - Math.max(1, h * 0.002), w, Math.max(2, h * 0.004));
      },
    };
  }

  // A line sweeps back and forth: the original on its left, the shapes on
  // its right. Periodic, so the reel also loops cleanly.
  function makeCurtain(src, b, interp) {
    const D = lengthOf(src);
    const pos = (t) => 0.5 + 0.4 * Math.sin((4 * Math.PI * t) / D); // two sweeps
    return {
      duration: D, videoTime: src.kind === "video" ? (t) => t : () => null, originalFrom: 0,
      paint(ctx, t, w, h) {
        if (src.kind === "image") drawShapes(ctx, src.image, w, h, Infinity);
        else Motion.render(ctx, src.anim, t, interp, w, h);
        drawCurtain(ctx, src, Math.round(w * pos(t)), w, h, 1);
      },
    };
  }

  // The original on the left of x, over what's drawn; the line and its
  // handle (with opacity a).
  function drawCurtain(ctx, src, x, w, h, a) {
    ctx.save();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.beginPath();
    ctx.rect(0, 0, x, h);
    ctx.clip();
    src.paintOriginal(ctx, 0, 0, w, h);
    ctx.restore();
    // The line and its handle.
    ctx.save();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.globalAlpha = a;
    ctx.shadowColor = "rgba(0,0,0,0.5)";
    ctx.shadowBlur = w * 0.01;
    ctx.fillStyle = "#fff";
    const lw = Math.max(2, w * 0.006);
    ctx.fillRect(x - lw / 2, 0, lw, h);
    const r = w * 0.045;
    ctx.beginPath();
    ctx.arc(x, h * 0.5, r, 0, 2 * Math.PI);
    ctx.fill();
    ctx.shadowColor = "transparent";
    ctx.fillStyle = "#222";
    for (const dir of [-1, 1]) {
      ctx.beginPath();
      ctx.moveTo(x + dir * r * 0.65, h * 0.5);
      ctx.lineTo(x + dir * r * 0.2, h * 0.5 - r * 0.35);
      ctx.lineTo(x + dir * r * 0.2, h * 0.5 + r * 0.35);
      ctx.fill();
    }
    ctx.restore();
  }

  // Counter first (a video: its first frame); once it's complete, the
  // curtain compares it with the original (a video plays meanwhile).
  function makeCountCurtain(src, b, interp) {
    const video = src.kind === "video";
    const L = video ? src.anim.duration : 0;
    const P = video ? clamp(0.35 * L, 3, 7) : 0.5 * src.duration; // when the curtain comes
    const D = video ? P + L : src.duration;
    const { plan, count } = countUp(video ? firstFrame(src.anim) : src.image, b, 0.85 * P - 0.3, D);
    // In from the left edge, then back and forth (a sweep and a half).
    const pos = (t) => {
      const u = (t - P) / (D - P);
      return smooth((t - P) / 0.8) * (0.5 - 0.42 * Math.cos(3 * Math.PI * u));
    };
    return {
      duration: D, videoTime: video ? (t) => (t < P ? null : t - P) : () => null, originalFrom: P,
      vars: (t) => ({ n: count(t) }),
      paint(ctx, t, w, h) {
        if (t < P || !video) plan.render(ctx, t, w, h);
        else Motion.render(ctx, src.anim, t - P, interp, w, h);
        if (t >= P) drawCurtain(ctx, src, Math.round(w * pos(t)), w, h, smooth((t - P) / 0.4));
      },
    };
  }

  // A counter builds the video's first frame; once it's complete, the video
  // starts with its shapes moving. At the end, the original.
  function makeCountVideo(src, b, interp) {
    const L = src.anim.duration, P = clamp(0.35 * L, 3, 7), D = P + L;
    const fade = clamp(0.2 * L, 0.5, 2);
    const [F, X] = finale(src, D, D - fade, fade);
    const { plan, count } = countUp(firstFrame(src.anim), b, 0.85 * P - 0.3, D);
    return {
      duration: D, videoTime: (t) => (t < P ? null : t - P), originalFrom: Math.max(F, P),
      vars: (t) => ({ n: count(t) }),
      paint(ctx, t, w, h) {
        if (t < P) plan.render(ctx, t, w, h);
        else Motion.render(ctx, src.anim, t - P, interp, w, h);
        if (t >= P) overlay(ctx, smooth((t - F) / X), () => src.paintOriginal(ctx, 0, 0, w, h));
      },
    };
  }

  // The original first; the shapes cover it one by one while it fades under
  // them, until only the shapes are left.
  function makeCover(src, b, interp) {
    const D = lengthOf(src), B = 0.75 * D, start = 0.4;
    const img = src.kind === "image" ? src.image : null;
    const plan = img && Build.plan(img, {
      direction: "build", order: b.order, curve: b.curve, effect: b.effect,
      effectTime: Math.min(b.effectTime, B * 0.3), duration: B, holdStart: start, holdEnd: D,
    });
    const ease = Build.CURVES[b.curve]?.f ?? ((p) => p); // a video: how many shapes are in
    const n = shapeCount(src);
    // The original fades under the shapes over the second part of the build.
    const veil = (t) => smooth((t - start - 0.3 * B) / (0.7 * B));
    return {
      duration: D, videoTime: src.kind === "video" ? (t) => t : () => null, originalFrom: 0,
      paint(ctx, t, w, h) {
        src.paintOriginal(ctx, 0, 0, w, h);
        const bg = img ? img.background : src.anim.background;
        overlay(ctx, veil(t), () => {
          ctx.setTransform(1, 0, 0, 1, 0, 0);
          ctx.fillStyle = bg;
          ctx.fillRect(0, 0, w, h);
        });
        if (img) plan.render(ctx, t, w, h, false);
        else Motion.render(ctx, src.anim, t, interp, w, h, Math.round(n * ease(clamp((t - start) / B, 0, 1))), false);
      },
    };
  }

  // The shapes of img built in B seconds (after a short hold) and, for a
  // counter, how many are in at t.
  function countUp(img, b, B, D) {
    const opts = {
      direction: "build", order: b.order, effect: b.effect, effectTime: Math.min(b.effectTime, B * 0.3), duration: B, holdStart: 0.3, holdEnd: D,
      // A video's frame has no scores to build "by impact" with.
      curve: b.curve === "impact" && !img.scores ? "inOutCubic" : b.curve,
    };
    const plan = Build.plan(img, opts);
    // When each shape starts, sorted, to count the ones already in.
    const sch = Build.schedule(img, opts, opts.effect === "none" ? 0 : opts.effectTime);
    const starts = img.shapes.map((_, i) => sch.startOf(i)).sort((a, c) => a - c);
    const count = (t) => {
      const bt = sch.buildTime(t);
      if (bt <= 0) return 0;
      let lo = 0, hi = starts.length;
      while (lo < hi) { const m = (lo + hi) >> 1; if (starts[m] <= bt) lo = m + 1; else hi = m; }
      return lo;
    };
    return { plan, count };
  }

  // A video's first keyframe as a photo's shapes.
  const firstFrame = (anim) => ({ width: anim.width, height: anim.height, background: anim.background, shapes: anim.keyframes[0] ?? [] });

  // The shapes add up with a counter ({n}); at the end, the photo.
  function makeCounter(src, b) {
    const D = src.duration;
    const [F, X] = finale(src, D, 0.86 * D, clamp(0.08 * D, 0.5, 1.2));
    const B = Math.min(0.7 * D, 0.8 * F); // counted up before the original comes
    const { plan, count } = countUp(src.image, b, B, D);
    return {
      duration: D, videoTime: () => null, originalFrom: F,
      vars: (t) => ({ n: count(t) }),
      paint(ctx, t, w, h) {
        plan.render(ctx, t, w, h);
        overlay(ctx, smooth((t - F) / X), () => src.paintOriginal(ctx, 0, 0, w, h));
      },
    };
  }

  // ---------- grids ----------

  // The four cells of a 2x2 grid over w x h, with a thin gap.
  function cells(w, h) {
    const g = Math.max(1, Math.round(w * 0.006)), cw = (w - g) / 2, ch = (h - g) / 2;
    return [[0, 0], [cw + g, 0], [0, ch + g], [cw + g, ch + g]].map(([x, y], i) => ({ x, y, w: cw, h: ch, i }));
  }

  // A small label hugging the center of the grid (out of the apps' zones):
  // under the top cells, over the bottom ones.
  function cellLabel(ctx, c, text, H, alpha) {
    if (alpha <= 0.002) return;
    ctx.save();
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    const size = Math.round(H * 0.024);
    ctx.font = `800 ${size}px ${FONT}`;
    const tw = ctx.measureText(text).width + size * 1.1, th = size * 1.5;
    const x = c.x + c.w / 2, y = c.i < 2 ? c.y + c.h - th - H * 0.012 : c.y + H * 0.012;
    ctx.globalAlpha = alpha;
    ctx.fillStyle = "rgba(0, 0, 0, 0.55)";
    ctx.beginPath();
    ctx.roundRect(x - tw / 2, y, tw, th, size * 0.4);
    ctx.fill();
    ctx.fillStyle = "#fff";
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    ctx.fillText(text, x, y + th / 2);
    ctx.restore();
  }

  // Nice counts between 10 and n for the mosaic (10, 50, 100, n...).
  function mosaicCounts(n) {
    if (n <= 12) return [1, Math.max(1, Math.round(n / 4)), Math.max(1, Math.round(n / 2)), n];
    const nice = (x) => {
      const k = 10 ** Math.floor(Math.log10(x));
      return [1, 2, 2.5, 5, 10].map((m) => m * k).reduce((a, b) => (Math.abs(Math.log(b / x)) < Math.abs(Math.log(a / x)) ? b : a));
    };
    return [0, 1, 2, 3].map((i) => (i === 3 ? n : Math.min(n, nice(10 * (n / 10) ** (i / 3)))));
  }

  // Cells with more and more shapes, appearing one after the other; at the
  // end the whole frame fades to the original.
  function makeMosaic(src, b, interp) {
    const D = lengthOf(src), counts = mosaicCounts(shapeCount(src)), scratch = new Map();
    const [R, X] = finale(src, D, 0.82 * D, clamp(0.08 * D, 0.5, 1.5));
    const stagger = Math.min(0.12 * D, Math.max(0.05, (R - 0.8) / 3.5)); // all four cells in before the original
    const shown = (i, t) => smooth((t - 0.2 - i * stagger) / 0.6);
    return {
      duration: D, videoTime: src.kind === "video" ? (t) => t : () => null, originalFrom: R,
      paint(ctx, t, w, h) {
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.fillStyle = "#111";
        ctx.fillRect(0, 0, w, h);
        for (const c of cells(w, h)) {
          const a = shown(c.i, t);
          if (a <= 0) continue;
          ctx.globalAlpha = a;
          inRect(ctx, c.x, c.y, c.w, c.h, scratch, (g, gw, gh) => (src.kind === "image"
            ? drawShapes(g, src.image, gw, gh, counts[c.i])
            : Motion.render(g, src.anim, t, interp, gw, gh, counts[c.i])));
          ctx.globalAlpha = 1;
          cellLabel(ctx, c, `${counts[c.i]} ${words(src.lang ?? "es").shapes}`, h, a * (1 - smooth((t - R) / X)));
        }
        overlay(ctx, smooth((t - R) / X), () => src.paintOriginal(ctx, 0, 0, w, h));
      },
    };
  }

  // The same photo with four kinds of shapes, building at the same time.
  function makePrefer(src, b) {
    const D = src.duration, scratch = new Map();
    const plans = src.variants.map((v) => Build.plan(v.image, {
      direction: "build", order: b.order, curve: b.curve, effect: b.effect,
      effectTime: Math.min(b.effectTime, D * 0.15), duration: 0.55 * D, holdStart: 0.3, holdEnd: D,
    }));
    const names = src.variants.map((v, i) => {
      const n = words(src.lang ?? "es").names[v.mode];
      return `${i + 1} · ${n[0].toUpperCase()}${n.slice(1)}`;
    });
    return {
      duration: D, videoTime: () => null, originalFrom: Infinity,
      paint(ctx, t, w, h) {
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.fillStyle = "#111";
        ctx.fillRect(0, 0, w, h);
        for (const c of cells(w, h)) {
          inRect(ctx, c.x, c.y, c.w, c.h, scratch, (g, gw, gh) => plans[c.i].render(g, t, gw, gh));
          cellLabel(ctx, c, names[c.i], h, smooth((t - 0.3) / 0.5));
        }
      },
    };
  }

  // ---------- brand ending ----------

  // A scene added after any template: a call to action, the logo built with
  // shapes, or the account. o: { kind: "cta" | "logo" | "handle", seconds,
  // text, handle, logo: { bg, image } }. paint(ctx, u, W, H, last) draws
  // second u of it over the whole frame; last(ctx) draws the reel's last
  // frame (behind the texts, and what the logo scene comes out of).
  const CTA = { es: "Seguime para ver más", en: "Follow for more", pt: "Me segue para ver mais", it: "Seguimi per vederne altri", fr: "Abonne-toi pour en voir plus", de: "Folge mir für mehr" };
  const OUTRO_KINDS = { none: "Ninguna", cta: "Frase (CTA)", logo: "Logo", handle: "@cuenta" };

  function outro(o, lang) {
    if (!o || !OUTRO_KINDS[o.kind] || o.kind === "none") return null;
    const O = clamp(+o.seconds || 2.5, 0.5, 8);
    const scratch = new Map();
    const image = o.kind === "logo" ? o.logo?.image : null;
    if (o.kind === "logo" && !image?.shapes?.length) return null;
    const plan = image && Build.plan(image, {
      direction: "build", order: "original", curve: "outCubic", effect: "pop", effectTime: Math.min(0.5, O * 0.25),
      duration: Math.max(0.3, O * 0.6), holdStart: 0.25, holdEnd: O,
    });
    const text = o.kind === "cta" ? (o.text?.trim() || CTA[lang] || CTA.en) : o.kind === "handle" ? (o.handle ?? "").trim() : "";
    return {
      duration: O,
      paint(ctx, u, W, H, last) {
        const inA = smooth(u / 0.4);
        if (plan) {
          // The logo's own color comes over the reel, and the logo builds in the middle.
          last(ctx);
          overlay(ctx, inA, () => {
            ctx.setTransform(1, 0, 0, 1, 0, 0);
            ctx.fillStyle = image.background;
            ctx.fillRect(0, 0, W, H);
          });
          const s = Math.min((W * 0.62) / image.width, (H * 0.4) / image.height);
          const w = image.width * s, h = image.height * s;
          ctx.save();
          ctx.globalAlpha = inA;
          // Only its shapes: the scene's color is its background, no square around it.
          inRect(ctx, (W - w) / 2, H * 0.45 - h / 2, w, h, scratch, (g, gw, gh) => {
            g.setTransform(1, 0, 0, 1, 0, 0);
            g.clearRect(0, 0, gw, gh);
            plan.render(g, u, gw, gh, false);
          });
          ctx.restore();
          return;
        }
        // Texts: over the reel's last frame, darkened.
        last(ctx);
        overlay(ctx, 0.6 * inA, () => {
          ctx.setTransform(1, 0, 0, 1, 0, 0);
          ctx.fillStyle = "#000";
          ctx.fillRect(0, 0, W, H);
        });
        if (!text) return;
        ctx.save();
        ctx.setTransform(1, 0, 0, 1, 0, 0);
        ctx.textAlign = "center";
        ctx.textBaseline = "top";
        ctx.fillStyle = "#fff";
        ctx.shadowColor = "rgba(0, 0, 0, 0.6)";
        ctx.shadowBlur = H * 0.012;
        const handle = o.kind === "handle";
        const a = smooth((u - 0.15) / 0.45);
        // The account pops in a little bigger; the phrase rises.
        const pop = handle ? 1 + 0.12 * (1 - a) : 1;
        ctx.translate(W / 2, H * 0.45);
        ctx.scale(pop, pop);
        block(ctx, text, {
          x: 0, y: 0, maxWidth: W * (1 - 2 * (SAFE.right + 0.02)), size: H * (handle ? 0.06 : 0.055), weight: 900,
          anchor: "center", maxLines: 3, alpha: a, rise: handle ? 0 : H * 0.02,
        });
        ctx.restore();
      },
    };
  }

  // ---------- phrases ----------

  // Base phrases of a template, keeping the texts written by hand (by role;
  // other templates' default texts don't carry over).
  function phrasesFor(id, current = []) {
    const defaults = new Set(Object.values(LIST).flatMap((tpl) => tpl.base.phrases.map((p) => p.text)));
    const texts = new Map(current.filter((p) => p.text.trim() && !defaults.has(p.text)).map((p) => [p.role, p.text]));
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
    counter: { size: 0.075, weight: 900, maxLines: 2 },
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
      half: { y: H * 0.52, anchor: "top", maxWidth: narrow },
      middle: { y: H * 0.45, anchor: "center", maxWidth: narrow },
      lower: { y: H * 0.66, anchor: "center", maxWidth: narrow },
      foot: { y: H * 0.755, anchor: "center", maxWidth: narrow },
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

  return { LIST, SAFE, POSITIONS, STYLES, SHAPE_NAMES, OUTRO_KINDS, CTA, words, auto, make, outro, phrasesFor, phraseAlpha, drawTexts };
})();
