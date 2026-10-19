"use strict";

// Animates the keyframes computed by the server (see encode.go) and exports
// them. Each shape is a flat array: [kind, r, g, b, a, fresh, ...params], in
// working-size pixels. Shape i of keyframe k+1 is shape i of keyframe k moved
// a little, unless it is marked fresh, in which case the two cross-fade.
const Motion = (() => {
  const POLY = 0, RECT = 1, ELL = 2, QUAD = 3;
  const H = 6; // header length
  const DEG = Math.PI / 180;

  const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x));

  // ---------- interpolation ----------

  const continues = (a, b) => b && !b[5] && b[0] === a[0] && b.length === a.length;

  function lerp(a, b, u) {
    const out = a.slice();
    for (let j = 1; j < a.length; j++) out[j] = a[j] + (b[j] - a[j]) * u;
    return out;
  }

  // Catmull-Rom through p0, p1, p2, p3, between p1 and p2: velocity is
  // continuous across keyframes, so motion doesn't stop and start at each one.
  function spline(p0, p1, p2, p3, u) {
    const u2 = u * u, u3 = u2 * u;
    const out = p1.slice();
    for (let j = 1; j < p1.length; j++) {
      out[j] = 0.5 * (2 * p1[j] + (p2[j] - p0[j]) * u +
        (2 * p0[j] - 5 * p1[j] + 4 * p2[j] - p3[j]) * u2 +
        (3 * p1[j] - p0[j] - 3 * p2[j] + p3[j]) * u3);
    }
    return out;
  }

  // Keep overshooting splines within valid ranges.
  function sanitize(s) {
    for (let j = 1; j <= 4; j++) s[j] = clamp(s[j], 0, 255);
    s[5] = 0;
    if (s[0] === RECT || s[0] === ELL) { s[H + 2] = Math.max(0, s[H + 2]); s[H + 3] = Math.max(0, s[H + 3]); }
    if (s[0] === QUAD) s[H + 6] = Math.max(0, s[H + 6]);
    return s;
  }

  // Shape i between keyframes k and k+1 (which must continue each other).
  function between(kfs, i, k, u, mode) {
    const a = kfs[k][i], b = kfs[k + 1][i];
    if (mode === "linear") return sanitize(lerp(a, b, u));
    const before = k > 0 && !a[5] ? kfs[k - 1][i] : null;
    const after = k + 2 < kfs.length ? kfs[k + 2][i] : null;
    const p0 = before && continues(before, a) ? before : a;
    const p3 = after && continues(b, after) ? after : b;
    return sanitize(spline(p0, a, b, p3, u));
  }

  // The shapes to draw at keyframe position x (fractional keyframe index), in
  // drawing order, each with an opacity factor: [[shape, factor], ...].
  function shapesAt(anim, x, mode) {
    const kfs = anim.keyframes;
    if (!kfs.length) return [];
    x = clamp(x, 0, kfs.length - 1);
    const k = Math.min(Math.floor(x), kfs.length - 1);
    const u = mode === "step" ? 0 : x - k;
    const A = kfs[k], B = kfs[k + 1];
    const out = [];
    for (let i = 0; i < A.length; i++) {
      const a = A[i], b = B?.[i];
      if (!b || u === 0) out.push([a, 1]);
      else if (continues(a, b)) out.push([between(kfs, i, k, u, mode), 1]);
      else out.push([a, 1 - u], [b, u]);
    }
    return out;
  }

  // ---------- canvas ----------

  function outputSize(anim, longSide) {
    const s = longSide / Math.max(anim.width, anim.height);
    return [Math.max(2, Math.round((anim.width * s) / 2) * 2), Math.max(2, Math.round((anim.height * s) / 2) * 2)];
  }

  function drawShape(ctx, s, factor) {
    const alpha = (s[4] / 255) * factor;
    if (alpha <= 0) return;
    const color = `rgba(${s[1] | 0},${s[2] | 0},${s[3] | 0},${alpha})`;
    switch (s[0]) {
      case POLY:
        ctx.beginPath();
        ctx.moveTo(s[H], s[H + 1]);
        for (let j = H + 2; j < s.length; j += 2) ctx.lineTo(s[j], s[j + 1]);
        ctx.closePath();
        ctx.fillStyle = color;
        ctx.fill();
        break;
      case RECT:
        ctx.save();
        ctx.translate(s[H], s[H + 1]);
        ctx.rotate(s[H + 4] * DEG);
        ctx.fillStyle = color;
        ctx.fillRect(-s[H + 2] / 2, -s[H + 3] / 2, s[H + 2], s[H + 3]);
        ctx.restore();
        break;
      case ELL:
        ctx.beginPath();
        ctx.ellipse(s[H], s[H + 1], s[H + 2], s[H + 3], s[H + 4] * DEG, 0, 2 * Math.PI);
        ctx.fillStyle = color;
        ctx.fill();
        break;
      case QUAD:
        ctx.beginPath();
        ctx.moveTo(s[H], s[H + 1]);
        ctx.quadraticCurveTo(s[H + 2], s[H + 3], s[H + 4], s[H + 5]);
        ctx.lineWidth = s[H + 6];
        ctx.lineCap = "round";
        ctx.lineJoin = "round";
        ctx.strokeStyle = color;
        ctx.stroke();
        break;
    }
  }

  // Draw the animation at time t (seconds) filling a w x h canvas.
  function render(ctx, anim, t, mode, w, h) {
    ctx.setTransform(1, 0, 0, 1, 0, 0);
    ctx.fillStyle = anim.background;
    ctx.fillRect(0, 0, w, h);
    const sx = w / anim.width, sy = h / anim.height;
    // Same half-pixel offset primitive uses when drawing.
    ctx.setTransform(sx, 0, 0, sy, 0.5 * sx, 0.5 * sy);
    for (const [s, f] of shapesAt(anim, t * anim.kfps, mode)) drawShape(ctx, s, f);
  }

  // ---------- MP4 export (WebCodecs) ----------

  // Lowest H.264 High-profile level that fits; [level_idc, max frame size in
  // macroblocks, max macroblocks per second].
  const AVC_LEVELS = [[0x28, 8192, 245760], [0x2a, 8192, 522240], [0x32, 22080, 589824], [0x33, 36864, 983040], [0x34, 36864, 2073600]];

  function avcCodec(w, h, fps) {
    const mbs = Math.ceil(w / 16) * Math.ceil(h / 16);
    const level = AVC_LEVELS.find(([, fs, rate]) => mbs <= fs && mbs * fps <= rate) ?? AVC_LEVELS.at(-1);
    return "avc1.6400" + level[0].toString(16).padStart(2, "0");
  }

  async function pickEncoder(w, h, fps) {
    if (!("VideoEncoder" in window)) throw new Error("este navegador no soporta WebCodecs (probá con Chrome, Edge o Safari)");
    const bitrate = Math.min(80e6, Math.round(w * h * fps * 0.15));
    const base = { width: w, height: h, framerate: fps, bitrate };
    const candidates = [
      { kind: "avc", config: { ...base, codec: avcCodec(w, h, fps), avc: { format: "avc" } } },
      { kind: "vp9", config: { ...base, codec: "vp09.00.51.08" } },
    ];
    for (const c of candidates) {
      try {
        if ((await VideoEncoder.isConfigSupported(c.config)).supported) return c;
      } catch { /* try the next one */ }
    }
    throw new Error("el navegador no puede codificar H.264 ni VP9 a este tamaño");
  }

  // Encode the keyframe animation. onProgress(fraction); signal aborts.
  function exportMP4(anim, opts) {
    return encodeMP4({
      width: anim.width, height: anim.height, duration: anim.duration,
      render: (ctx, t, w, h) => render(ctx, anim, t, opts.mode, w, h),
    }, opts);
  }

  // Encode any source { width, height, duration, render(ctx, t, w, h) }; an
  // optional async prepare(t) runs before each frame (e.g. to seek a video).
  async function encodeMP4(src, { fps, longSide, onProgress, signal }) {
    const [w, h] = outputSize(src, longSide);
    const { kind, config } = await pickEncoder(w, h, fps);
    const samples = [];
    let description = null;
    let failure = null;
    const encoder = new VideoEncoder({
      output(chunk, meta) {
        if (meta?.decoderConfig?.description) description = meta.decoderConfig.description;
        const data = new Uint8Array(chunk.byteLength);
        chunk.copyTo(data);
        samples.push({ data, pts: chunk.timestamp, key: chunk.type === "key" });
      },
      error(e) { failure = e; },
    });
    encoder.configure(config);

    const canvas = typeof OffscreenCanvas !== "undefined" ? new OffscreenCanvas(w, h) : Object.assign(document.createElement("canvas"), { width: w, height: h });
    const ctx = canvas.getContext("2d", { alpha: false });
    const total = Math.max(1, Math.ceil(src.duration * fps));
    try {
      for (let f = 0; f < total; f++) {
        if (signal?.aborted) throw new DOMException("cancelado", "AbortError");
        if (failure) throw failure;
        if (src.prepare) await src.prepare(f / fps);
        src.render(ctx, f / fps, w, h);
        const frame = new VideoFrame(canvas, { timestamp: Math.round((f * 1e6) / fps), duration: Math.round(1e6 / fps) });
        encoder.encode(frame, { keyFrame: f % Math.round(fps * 2) === 0 });
        frame.close();
        while (encoder.encodeQueueSize > 4) await new Promise((r) => setTimeout(r, 1));
        if (f % 10 === 0) { onProgress?.(f / total); await new Promise((r) => setTimeout(r, 0)); }
      }
      await encoder.flush();
      if (failure) throw failure;
    } finally {
      if (encoder.state !== "closed") encoder.close();
    }
    onProgress?.(1);
    return { blob: Mp4.write({ codec: kind, width: w, height: h, fps, description, samples }), codec: kind, width: w, height: h };
  }

  // ---------- Lottie export ----------

  // A "life" is a run of keyframes in which shape i keeps being the same shape.
  function lives(anim) {
    const kfs = anim.keyframes, out = [];
    const count = kfs[0]?.length ?? 0;
    for (let i = 0; i < count; i++) {
      let start = 0;
      for (let k = 1; k <= kfs.length; k++) {
        if (k === kfs.length || !continues(kfs[k - 1][i], kfs[k][i])) {
          out.push({ i, start, end: k - 1 });
          start = k;
        }
      }
    }
    return out.sort((a, b) => a.i - b.i || a.start - b.start); // drawing order
  }

  const hex2 = (n) => n.toString(16).padStart(2, "0");

  function lottie(anim, { mode, fps, longSide }) {
    const [w, h] = outputSize(anim, longSide);
    const sx = w / anim.width, sy = h / anim.height;
    const kfs = anim.keyframes;
    const n = kfs.length;
    const op = Math.max(1, Math.round(anim.duration * fps));
    const frameOf = (k) => (k / anim.kfps) * fps;
    const X = (x) => +((x + 0.5) * sx).toFixed(2);
    const Y = (y) => +((y + 0.5) * sy).toFixed(2);
    const r3 = (v) => +v.toFixed(3);
    // Smooth motion is baked as extra linear keys between keyframes.
    const sub = mode === "smooth" ? 3 : 1;
    const ease = mode === "step" ? { h: 1 } : { i: { x: [1], y: [1] }, o: { x: [0], y: [0] } };

    // Animated (or static) property from [frame, value] samples.
    const prop = (keys) => {
      const same = keys.every(([, v]) => JSON.stringify(v) === JSON.stringify(keys[0][1]));
      // Static values are bare: a number for 1-D properties, an object for paths.
      if (same) return { a: 0, k: keys[0][1].length === 1 ? keys[0][1][0] : keys[0][1] };
      return { a: 1, k: keys.map(([t, v], j) => (j < keys.length - 1 ? { t: r3(t), s: v, ...ease } : { t: r3(t), s: v })) };
    };
    const staticTr = (extra = {}) => ({
      ty: "tr", p: { a: 0, k: [0, 0] }, a: { a: 0, k: [0, 0] }, s: { a: 0, k: [100, 100] },
      r: { a: 0, k: 0 }, o: { a: 0, k: 100 }, sk: { a: 0, k: 0 }, sa: { a: 0, k: 0 }, ...extra,
    });
    const v2 = (x, y) => [X(x), Y(y)];
    const pathOf = (s) => {
      if (s[0] === QUAD) {
        const [x1, y1, cx, cy, x2, y2] = s.slice(H, H + 6);
        return [{
          c: false,
          v: [v2(x1, y1), v2(x2, y2)],
          o: [[r3((2 / 3) * (cx - x1) * sx), r3((2 / 3) * (cy - y1) * sy)], [0, 0]],
          i: [[0, 0], [r3((2 / 3) * (cx - x2) * sx), r3((2 / 3) * (cy - y2) * sy)]],
        }];
      }
      const v = [];
      for (let j = H; j < s.length; j += 2) v.push(v2(s[j], s[j + 1]));
      return [{ c: true, v, i: v.map(() => [0, 0]), o: v.map(() => [0, 0]) }];
    };

    const layers = [];
    for (const life of lives(anim)) {
      // Sample the shape along its life.
      const samples = [];
      for (let k = life.start; k <= life.end; k++) {
        const steps = k < life.end ? sub : 1;
        for (let q = 0; q < steps; q++) {
          const u = q / steps;
          samples.push([frameOf(k + u), u === 0 ? kfs[k][life.i] : between(kfs, life.i, k, u, mode)]);
        }
      }
      const kind = samples[0][1][0];
      const color = prop(samples.map(([t, s]) => [t, [r3(s[1] / 255), r3(s[2] / 255), r3(s[3] / 255), 1]]));
      const alpha = prop(samples.map(([t, s]) => [t, [r3((s[4] / 255) * 100)]]));
      let items;
      if (kind === RECT || kind === ELL) {
        const size = prop(samples.map(([t, s]) => [t, kind === RECT ? [r3(s[H + 2] * sx), r3(s[H + 3] * sy)] : [r3(2 * s[H + 2] * sx), r3(2 * s[H + 3] * sy)]]));
        items = [
          kind === RECT ? { ty: "rc", d: 1, s: size, p: { a: 0, k: [0, 0] }, r: { a: 0, k: 0 } } : { ty: "el", d: 1, s: size, p: { a: 0, k: [0, 0] } },
          { ty: "fl", c: color, o: alpha, r: 1 },
          staticTr({
            p: prop(samples.map(([t, s]) => [t, v2(s[H], s[H + 1])])),
            r: prop(samples.map(([t, s]) => [t, [r3(s[H + 4])]])),
          }),
        ];
      } else {
        const paint = kind === QUAD
          ? { ty: "st", c: color, o: alpha, w: prop(samples.map(([t, s]) => [t, [r3(s[H + 6] * sx)]])), lc: 2, lj: 2 }
          : { ty: "fl", c: color, o: alpha, r: 1 };
        items = [{ ty: "sh", ks: prop(samples.map(([t, s]) => [t, pathOf(s)])) }, paint, staticTr()];
      }

      // Cross-fades with the shapes before and after, like the player.
      const fadeIn = life.start > 0, fadeOut = life.end < n - 1;
      const ip = fadeIn ? frameOf(life.start - 1) : 0;
      const outAt = fadeOut ? frameOf(life.end + 1) : op;
      const opacity = [];
      if (fadeIn) opacity.push([ip, [0]], [frameOf(life.start), [100]]);
      if (fadeOut) opacity.push([frameOf(life.end), [100]], [outAt, [0]]);
      layers.push({
        ddd: 0, ty: 4, nm: `forma ${life.i + 1}`, sr: 1, ao: 0, bm: 0, st: 0,
        ip: r3(ip), op: r3(Math.min(op, outAt)),
        ks: {
          o: opacity.length ? { a: 1, k: opacity.map(([t, v], j) => (j < opacity.length - 1 ? { t: r3(t), s: v, ...ease } : { t: r3(t), s: v })) } : { a: 0, k: 100 },
          r: { a: 0, k: 0 }, p: { a: 0, k: [0, 0, 0] }, a: { a: 0, k: [0, 0, 0] }, s: { a: 0, k: [100, 100, 100] },
        },
        shapes: [{ ty: "gr", nm: "forma", it: items }],
      });
    }
    // The background goes under everything.
    layers.unshift({
      ddd: 0, ty: 1, nm: "fondo", sr: 1, ao: 0, bm: 0, st: 0, ip: 0, op,
      sc: anim.background, sw: w, sh: h,
      ks: { o: { a: 0, k: 100 }, r: { a: 0, k: 0 }, p: { a: 0, k: [0, 0, 0] }, a: { a: 0, k: [0, 0, 0] }, s: { a: 0, k: [100, 100, 100] } },
    });
    // Lottie lists the top layer first.
    layers.reverse().forEach((l, j) => { l.ind = j + 1; });
    return { v: "5.7.4", fr: fps, ip: 0, op, w, h, nm: "To-Primitive", ddd: 0, assets: [], layers };
  }

  return { shapesAt, render, drawShape, outputSize, exportMP4, encodeMP4, lottie };
})();
