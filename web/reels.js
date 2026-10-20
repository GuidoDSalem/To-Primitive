"use strict";

// Reel editor: a photo or a portion of a video, framed vertically (9:16),
// turned into shapes and played through a template with texts on top.

const $ = (id) => document.getElementById(id);
const ASPECT = 9 / 16;
const MAX_PORTION = 60; // seconds of video per reel
const clamp = (x, lo, hi) => Math.min(hi, Math.max(lo, x));
const fmtTime = (s) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}.${Math.floor((s % 1) * 10)}`;

async function api(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  return res.headers.get("Content-Type")?.includes("json") ? res.json() : res;
}

const reel = {
  kind: null,       // "image" | "video"
  name: "",
  url: null,        // object URL of the local file
  img: null,        // the photo
  video: null,      // { id, info } of the uploaded video
  portion: { start: 0, end: 0 },
  framing: { mode: "fill", zoom: 1, px: 0.5, py: 0.5 },
  template: "auto",
  duration: { preset: "15", free: 20 },
  audio: { mode: "none", file: null, url: null }, // mode: "none" | "source" | "file"
  phrases: [],      // see Templates: { role, text, from, to, pos, style, extend? }
  touched: new Set(), // settings changed by hand: a template's base doesn't override them
  gen: null,        // generated shapes: { key, image } (photo) or { key, anim } (video)
  generating: null, // { cancel() } while generating
};
const srcVideo = $("srcVideo");

// ---------- framing ----------

function sourceSize() {
  if (reel.kind === "image") return [reel.img.naturalWidth, reel.img.naturalHeight];
  if (reel.kind === "video") return [srcVideo.videoWidth || reel.video.info.width, srcVideo.videoHeight || reel.video.info.height];
  return [0, 0];
}

// The region of the source shown in the frame, as fractions; null when the
// whole source is fitted ("fit").
function crop() {
  if (reel.framing.mode !== "fill") return null;
  const [w, h] = sourceSize();
  if (!w || !h) return null;
  let bw = w, bh = w / ASPECT; // largest 9:16 rectangle inside the source
  if (bh > h) { bh = h; bw = h * ASPECT; }
  const cw = bw / reel.framing.zoom, ch = bh / reel.framing.zoom;
  const r = (v) => Math.round(v * 1e5) / 1e5;
  return { x: r(((w - cw) * reel.framing.px) / w), y: r(((h - ch) * reel.framing.py) / h), w: r(cw / w), h: r(ch / h) };
}

// Aspect (width / height) of what the content covers.
function contentAspect() {
  if (reel.framing.mode === "fill") return ASPECT;
  const [w, h] = sourceSize();
  return w && h ? w / h : ASPECT;
}

// Scratch canvases for drawFramed, one set per target so that drawing the
// preview and the template cards doesn't keep resizing the same ones.
const buffers = () => ({ layer: document.createElement("canvas"), tiny: document.createElement("canvas") });
const previewBufs = buffers();

// Draw a frame of the reel: paint(ctx, w, h) draws the content filling w x h.
// In "fit" mode the content is letterboxed over a blurred, darkened copy of
// itself (scaling a tiny copy up blurs it for free).
function drawFramed(ctx, W, H, paint, bufs = previewBufs) {
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  if (reel.framing.mode === "fill") {
    paint(ctx, W, H);
    return;
  }
  const { layer, tiny } = bufs;
  const aspect = contentAspect();
  let lw = W, lh = W / aspect;
  if (lh > H) { lh = H; lw = H * aspect; }
  lw = Math.max(1, Math.round(lw));
  lh = Math.max(1, Math.round(lh));
  if (layer.width !== lw || layer.height !== lh) { layer.width = lw; layer.height = lh; }
  const lc = layer.getContext("2d");
  lc.setTransform(1, 0, 0, 1, 0, 0);
  paint(lc, lw, lh);

  tiny.width = 24;
  tiny.height = Math.max(1, Math.round(24 / aspect));
  tiny.getContext("2d").drawImage(layer, 0, 0, tiny.width, tiny.height);
  const s = Math.max(W / lw, H / lh) * 1.1;
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(tiny, (W - lw * s) / 2, (H - lh * s) / 2, lw * s, lh * s);
  ctx.fillStyle = "rgba(0,0,0,0.35)";
  ctx.fillRect(0, 0, W, H);
  ctx.drawImage(layer, Math.round((W - lw) / 2), Math.round((H - lh) / 2));
}

// The original (photo, or the video's current frame) with the current
// framing. over: draw only the picture, on top of what's there (fades).
function paintOriginal(ctx, w, h, over = false) {
  const el = reel.kind === "image" ? reel.img : srcVideo;
  const [sw, sh] = sourceSize();
  const c = crop() ?? { x: 0, y: 0, w: 1, h: 1 };
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  if (!over) {
    ctx.fillStyle = "#000";
    ctx.fillRect(0, 0, w, h);
  }
  if (sw && (reel.kind === "image" || srcVideo.readyState >= 2)) {
    ctx.drawImage(el, c.x * sw, c.y * sh, c.w * sw, c.h * sh, 0, 0, w, h);
  }
}

// ---------- template, duration and texts ----------

const templateId = () => (reel.template === "auto" ? Templates.auto(reel.kind) : reel.template);
// Values the phrases can mention: "{formas} {tipo}" → "150 triángulos".
const textVars = () => ({ formas: params().shapes, tipo: Templates.SHAPE_NAMES[params().mode] ?? "formas" });

// Requested length of the reel, in seconds.
function reelSeconds() {
  const d = reel.duration;
  return d.preset === "free" ? clamp(+d.free || 15, 3, 90) : +d.preset;
}

// For videos the duration is set through the portion: the loop plays it
// forward and back, so it takes half.
const portionFactor = () => (templateId() === "loop" ? 2 : 1);

// The template applied to the generated shapes, or null when there's
// nothing (current) to show. Cached per shapes, template and duration.
function composition(id = templateId()) {
  if (!reel.gen || stale() || !Templates.LIST[id].kinds.includes(reel.kind)) return null;
  const build = buildOpts(), interp = $("interp").value;
  const key = JSON.stringify([id, reel.kind === "image" ? reelSeconds() : 0, build, interp]);
  const cache = (reel.gen.comps ??= new Map());
  if (!cache.has(key)) {
    cache.set(key, Templates.make(id, {
      kind: reel.kind, image: reel.gen.image, anim: reel.gen.anim, duration: reelSeconds(), build, interp,
      paintOriginal: (ctx, w, h) => paintOriginal(ctx, w, h, true),
    }));
  }
  return cache.get(key);
}

// A full frame of the reel at time t: framed content plus texts.
function renderReel(ctx, comp, t, W, H, bufs) {
  drawFramed(ctx, W, H, (c, w, h) => comp.paint(c, t, w, h), bufs);
  // Another template (a card) shows with its own phrases, carrying the texts.
  const phrases = comp.id === templateId() ? reel.phrases : Templates.phrasesFor(comp.id, reel.phrases);
  Templates.drawTexts(ctx, W, H, t, comp.duration, phrases, textVars(), $("tBrand").value);
}

// ---------- shape parameters (as in the editor) ----------

function params() {
  return {
    shapes: +$("shapes").value,
    mode: +document.querySelector("#mode .on").dataset.value,
    alpha: $("alphaAuto").checked ? 0 : +$("alpha").value,
    repeat: +$("repeat").value,
    inputSize: +$("inputSize").value,
    background: $("bgAuto").checked ? "" : $("background").value,
  };
}

// Video-only computation settings.
const videoOpts = () => ({ kfps: +$("kfps").value, adapt: +$("adapt").value, flow: $("videoFlow").checked });

// How the photo's shapes appear (the editor's recording options).
const buildOpts = () => ({
  order: $("recOrder").value, curve: $("recCurve").value, effect: $("recEffect").value, effectTime: +$("recEffectTime").value,
});

// Everything the reel's shapes depend on: when it changes, they must be
// found again (photos: automatically; videos: "Calcular el video").
function genKey() {
  return JSON.stringify({
    kind: reel.kind, name: reel.name, mode: reel.framing.mode, crop: crop(), params: params(),
    video: reel.kind === "video" ? { portion: reel.portion, ...videoOpts() } : null,
  });
}
const stale = () => !!reel.gen && reel.gen.key !== genKey();

// ---------- live shapes ----------

// Like the editor: a session on the framed photo (or on a test frame of the
// video) keeps finding shapes, and every parameter change shows at once.
// For photos, its finished result is the reel's shapes.
const live = {
  token: 0,       // bumps on every new session, to drop stale answers
  id: null, es: null,
  key: null,      // the image it works on (source, framing, test frame)
  g: null,        // current run of the session
  img: null,      // { width, height, background, shapes, scores, baseScore }
  running: false, count: 0, paused: false,
  runKey: null,   // params() the current run is known to follow
  sentKey: null,  // params() last sent
  waitG: null,    // after a restarting change: the old run, until a new one starts
};
const paramsKey = () => JSON.stringify(params());
const algoKey = (k) => { const p = JSON.parse(k); delete p.shapes; return JSON.stringify(p); };

function testTime() {
  return reel.kind === "video" ? clamp(+$("testFrame").value, reel.portion.start, reel.portion.end) : 0;
}
const liveImageKey = () => JSON.stringify({ name: reel.name, url: reel.url, mode: reel.framing.mode, crop: crop(), t: testTime() });

function closeLive() {
  live.es?.close();
  if (live.id) {
    // Free the CPU: the server drops old sessions by itself later.
    api(`/api/sessions/${live.id}/params`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...params(), paused: true }) }).catch(() => {});
  }
  Object.assign(live, { token: live.token + 1, id: null, es: null, key: null, g: null, img: null, running: false, count: 0, runKey: null, sentKey: null, waitG: null });
}

// The framed photo as a JPEG (the session works on exactly that).
async function framedPhoto() {
  const [w, h] = sourceSize();
  const c = crop() ?? { x: 0, y: 0, w: 1, h: 1 };
  const sw = c.w * w, sh = c.h * h;
  const scale = Math.min(1, 1024 / Math.max(sw, sh));
  const cv = document.createElement("canvas");
  cv.width = Math.max(1, Math.round(sw * scale));
  cv.height = Math.max(1, Math.round(sh * scale));
  cv.getContext("2d").drawImage(reel.img, c.x * w, c.y * h, sw, sh, 0, 0, cv.width, cv.height);
  return new Promise((r) => cv.toBlob(r, "image/jpeg", 0.92));
}

async function startLive() {
  if (!reel.kind) return;
  const key = liveImageKey();
  if (live.key === key && live.id) return;
  closeLive();
  live.key = key;
  const token = live.token;
  const p = { ...params(), paused: live.paused };
  let d;
  try {
    if (reel.kind === "image") {
      const form = new FormData();
      form.append("image", await framedPhoto(), "reel.jpg");
      form.append("params", JSON.stringify(p));
      d = await api("/api/sessions", { method: "POST", body: form });
    } else {
      d = await api(`/api/videos/${reel.video.id}/frames`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ t: testTime(), params: p, crop: crop() }),
      });
    }
  } catch (e) {
    if (token === live.token) $("liveStatus").textContent = "Error: " + e.message;
    return;
  }
  if (token !== live.token) return; // superseded meanwhile
  live.id = d.id;
  live.sentKey = live.runKey = paramsKey();
  const es = new EventSource(`/api/sessions/${d.id}/events`);
  live.es = es;
  es.addEventListener("snapshot", (e) => {
    const s = JSON.parse(e.data);
    if (live.waitG !== null && s.g !== live.waitG) {
      live.waitG = null;
      live.runKey = live.sentKey;
    }
    live.g = s.g;
    live.img = { width: s.width, height: s.height, background: s.background, baseScore: s.baseScore, shapes: s.data.slice(), scores: s.scores.slice() };
    live.running = s.running;
    live.count = s.data.length;
    if ($("bgAuto").checked) $("background").value = s.background;
    liveChanged();
  });
  es.addEventListener("shape", (e) => {
    const s = JSON.parse(e.data);
    if (s.g !== live.g || !live.img) return;
    live.img.shapes[s.i] = s.data;
    live.img.scores[s.i] = s.score;
    live.count = Math.max(live.count, s.i + 1);
    liveChanged();
  });
  es.addEventListener("status", (e) => {
    const s = JSON.parse(e.data);
    if (s.g !== live.g) return;
    live.running = s.running;
    live.count = s.count;
    liveChanged();
  });
}

// Reframing or another test frame needs a new session (once the hand stops).
let restartTimer = 0;
function restartLiveSoon() {
  clearTimeout(restartTimer);
  restartTimer = setTimeout(startLive, 350);
}

// Parameter changes go to the running session, as in the editor.
let paramsTimer = 0;
function sendParams() {
  refreshUI();
  if (!live.id) return;
  clearTimeout(paramsTimer);
  paramsTimer = setTimeout(async () => {
    const id = live.id, key = paramsKey();
    if (!id || key === live.sentKey) return;
    const restarts = algoKey(key) !== algoKey(live.sentKey);
    live.sentKey = key;
    try {
      await api(`/api/sessions/${id}/params`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...params(), paused: live.paused }) });
    } catch { return; }
    if (id !== live.id || key !== live.sentKey) return;
    if (restarts) live.waitG = live.g; // the new run announces itself with a snapshot
    else live.runKey = key;
    liveChanged();
  }, 120);
}

// The live session moved on: redraw, and for photos take its finished
// result as the reel's shapes.
let liveDraw = 0;
function liveChanged() {
  const target = params().shapes;
  if (reel.kind === "image" && live.img && !live.running && live.count >= target &&
      live.key === liveImageKey() && live.runKey === paramsKey() && live.waitG === null) {
    const key = genKey();
    if (reel.gen?.key !== key) {
      const im = live.img;
      reel.gen = { key, image: { ...im, shapes: im.shapes.slice(0, target), scores: im.scores.slice(0, target) } };
      Picker.refresh();
      // The first time, show the reel right away.
      if (!reel.shown) {
        reel.shown = true;
        setView("reel");
        player.seek(0);
        player.play();
      }
    }
  }
  if (!liveDraw) {
    liveDraw = requestAnimationFrame(() => {
      liveDraw = 0;
      refreshUI();
      if (!player.playing) player.draw();
      Picker.refreshSoon();
    });
  }
}

$("pause").addEventListener("click", () => {
  live.paused = !live.paused;
  if (live.id) {
    api(`/api/sessions/${live.id}/params`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...params(), paused: live.paused }) }).catch(() => {});
  }
  refreshUI();
});

// The live shapes, filling w x h.
function paintLive(ctx, w, h) {
  const im = live.img;
  if (!im) {
    paintOriginal(ctx, w, h);
    return;
  }
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  ctx.fillStyle = im.background;
  ctx.fillRect(0, 0, w, h);
  const sx = w / im.width, sy = h / im.height;
  ctx.setTransform(sx, 0, 0, sy, 0.5 * sx, 0.5 * sy);
  const n = Math.min(params().shapes, im.shapes.length);
  for (let i = 0; i < n; i++) if (im.shapes[i]) Motion.drawShape(ctx, im.shapes[i], 1);
  ctx.setTransform(1, 0, 0, 1, 0, 0);
}

// ---------- video computation ----------

function setProgress(fraction, text) {
  $("genProgress").hidden = fraction === null;
  if (fraction !== null) $("genProgress").value = fraction;
  $("genStatus").textContent = text;
}

// Compute the keyframes for the chosen portion and framing.
async function generateVideo(p) {
  const v = reel.video;
  const body = {
    ...p, ...videoOpts(),
    start: reel.portion.start, length: reel.portion.end - reel.portion.start, crop: crop(),
  };
  return new Promise((resolve, reject) => {
    const es = new EventSource(`/api/videos/${v.id}/events`);
    let armed = false;
    const fail = (err) => { es.close(); reject(err); };
    es.addEventListener("job", async (e) => {
      const j = JSON.parse(e.data);
      if (!armed) {
        // The first event is the snapshot of an earlier job: start ours now.
        armed = true;
        try {
          await api(`/api/videos/${v.id}/compute`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
        } catch (err) {
          fail(err);
        }
        return;
      }
      if (!j) return;
      if (j.state === "running") {
        setProgress(j.total ? j.frame / j.total : 0, `Calculando keyframes: ${j.frame} / ${j.total}`);
      } else if (j.state === "done") {
        es.close();
        try {
          resolve(await api(`/api/videos/${v.id}/keyframes`));
        } catch (err) {
          reject(err);
        }
      } else {
        fail(new Error(j.error || "cancelado"));
      }
    });
    reel.generating = {
      cancel: () => {
        api(`/api/videos/${v.id}/compute`, { method: "DELETE" }).catch(() => {});
        fail(new DOMException("cancelado", "AbortError"));
      },
    };
  });
}

$("generate").addEventListener("click", async () => {
  if (reel.generating) {
    reel.generating.cancel();
    return;
  }
  const key = genKey();
  $("generate").textContent = "Cancelar";
  setProgress(0, "Preparando…");
  // Free the CPU for the computation, as the editor does.
  if (!live.paused) $("pause").click();
  const started = performance.now();
  try {
    reel.gen = { key, anim: await generateVideo(params()) };
    setProgress(null, `Listo en ${((performance.now() - started) / 1000).toFixed(1)} s.`);
    setView("reel");
    player.seek(0);
    player.play();
  } catch (e) {
    setProgress(null, e.name === "AbortError" ? "Cancelado." : "Error: " + e.message);
  } finally {
    reel.generating = null;
    $("generate").textContent = "Calcular el video";
    refreshUI();
  }
});

// ---------- preview player ----------

let view = "shapes"; // what the preview shows: "reel", "shapes" (live) or "original"

function setView(v) {
  view = v;
  for (const b of $("viewMode").children) b.classList.toggle("on", b.dataset.value === v);
  player.draw();
}
$("viewMode").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b || b.disabled) return;
  reel.backToReel = false; // the person chose
  setView(b.dataset.value);
});

const shownReel = () => (view === "reel" ? composition() : null);
const portionLength = () => Math.max(0.1, reel.portion.end - reel.portion.start);

function reelDuration() {
  const comp = shownReel();
  if (comp) return comp.duration;
  return reel.kind === "video" ? portionLength() : reelSeconds();
}

const player = (() => {
  const canvas = $("preview");
  const ctx = canvas.getContext("2d", { alpha: false });
  let t = 0, playing = false, last = 0, raf = 0;

  // Keep the hidden <video> on the frame the preview needs at time t.
  function syncVideo(play) {
    if (reel.kind !== "video") return;
    const comp = shownReel();
    const vt = comp ? comp.videoTime(t) : view === "original" ? t : null;
    if (vt === null) {
      srcVideo.pause();
      return;
    }
    const want = reel.portion.start + Math.min(vt, portionLength() - 0.04);
    if (play) {
      if (Math.abs(srcVideo.currentTime - want) > 0.25) srcVideo.currentTime = want;
      if (srcVideo.paused) srcVideo.play().catch(() => {});
    } else {
      srcVideo.pause();
      if (Math.abs(srcVideo.currentTime - want) > 0.01) srcVideo.currentTime = want;
    }
  }

  // The reel's sound while it plays (approximate: the export mixes it on
  // the server, with the exact fade).
  const audio = $("audioPreview");
  function syncAudio(play) {
    const a = audioSource();
    if (!play || !a || !shownReel()) {
      audio.pause();
      return;
    }
    if (audio.dataset.src !== a.url) {
      audio.src = a.url;
      audio.dataset.src = a.url;
    }
    const want = a.start + t;
    if (Math.abs(audio.currentTime - want) > 0.25) audio.currentTime = want;
    const dur = reelDuration(), fade = +$("fadeOut").value;
    const fadeGain = fade > 0 ? clamp((dur - t) / fade, 0, 1) : 1;
    audio.volume = clamp(+$("volume").value, 0, 1) * fadeGain;
    if (audio.paused) audio.play().catch(() => {});
  }

  function draw() {
    const box = $("phone");
    const dpr = window.devicePixelRatio || 1;
    const w = Math.max(9, Math.round(box.clientWidth * dpr)), h = Math.max(16, Math.round(box.clientHeight * dpr));
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
      $("overlay").width = w;
      $("overlay").height = h;
      drawOverlay();
    }
    const dur = reelDuration();
    t = clamp(t, 0, dur);
    const comp = shownReel();
    if (!reel.kind) {
      ctx.setTransform(1, 0, 0, 1, 0, 0);
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, w, h);
    } else if (comp) {
      renderReel(ctx, comp, t, w, h);
    } else if (view === "shapes") {
      drawFramed(ctx, w, h, paintLive);
    } else {
      drawFramed(ctx, w, h, paintOriginal);
    }
    $("seek").max = dur;
    $("seek").value = t;
    $("clock").textContent = `${fmtTime(t)} / ${fmtTime(dur)}`;
  }

  function tick(now) {
    raf = 0;
    if (!playing) return;
    t += (now - last) / 1000;
    last = now;
    if (t >= reelDuration()) t = 0;
    syncVideo(true);
    syncAudio(true);
    draw();
    raf = requestAnimationFrame(tick);
  }

  function play() {
    if (!reel.kind) return;
    playing = true;
    $("play").textContent = "❚❚";
    last = performance.now();
    syncVideo(true);
    syncAudio(true);
    if (!raf) raf = requestAnimationFrame(tick);
  }

  function pause() {
    playing = false;
    $("play").textContent = "▶";
    syncVideo(false);
    syncAudio(false);
    draw();
  }

  function seek(x) {
    t = clamp(x, 0, reelDuration());
    syncVideo(playing);
    syncAudio(playing);
    draw();
  }

  $("play").addEventListener("click", () => (playing ? pause() : play()));
  $("seek").addEventListener("input", () => seek(+$("seek").value));
  srcVideo.addEventListener("seeked", () => { if (!playing) draw(); });
  srcVideo.addEventListener("loadeddata", () => draw());
  new ResizeObserver(() => draw()).observe($("phone"));
  document.addEventListener("keydown", (e) => {
    if (e.code === "Space" && e.target === document.body && reel.kind) {
      e.preventDefault();
      playing ? pause() : play();
    }
  });
  return { draw, play, pause, seek, get playing() { return playing; }, get time() { return t; } };
})();

// Areas the apps cover with their own buttons and captions.
function drawOverlay() {
  const SAFE = Templates.SAFE;
  const c = $("overlay"), g = c.getContext("2d");
  const w = c.width, h = c.height;
  g.clearRect(0, 0, w, h);
  if (!$("safeZones").checked || !reel.kind) return;
  g.fillStyle = "rgba(255, 90, 90, 0.18)";
  g.strokeStyle = "rgba(255, 120, 120, 0.7)";
  g.lineWidth = Math.max(1, w / 400);
  g.setLineDash([w / 80, w / 120]);
  const zones = [
    [0, 0, w, h * SAFE.top, "Barra superior"],
    [0, h * (1 - SAFE.bottom), w, h * SAFE.bottom, "Texto y nombre de la cuenta"],
    [w * (1 - SAFE.right), h * SAFE.rightFrom, w * SAFE.right, h * (1 - SAFE.bottom - SAFE.rightFrom), "Botones"],
  ];
  g.font = `${Math.round(h / 60)}px system-ui, sans-serif`;
  for (const [x, y, zw, zh, label] of zones) {
    g.fillRect(x, y, zw, zh);
    g.strokeRect(x, y, zw, zh);
    g.save();
    g.fillStyle = "rgba(255, 220, 220, 0.85)";
    if (zw < w / 2) {
      g.translate(x + zw / 2 + h / 160, y + zh / 2);
      g.rotate(-Math.PI / 2);
      g.textAlign = "center";
      g.fillText(label, 0, 0);
    } else {
      g.fillText(label, x + w / 40, y + zh / 2 + h / 160);
    }
    g.restore();
  }
}
$("safeZones").addEventListener("input", drawOverlay);

// ---------- template cards ----------

const cards = (() => {
  const list = [];
  for (const id of ["auto", ...Object.keys(Templates.LIST)]) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "tpl";
    b.dataset.value = id;
    const canvas = document.createElement("canvas");
    canvas.width = 108;
    canvas.height = 192;
    const label = document.createElement("span");
    b.append(canvas, label);
    $("templates").append(b);
    b.addEventListener("click", () => {
      reel.template = id;
      applyBase();
      if (reel.kind === "video") applyDuration();
      refreshUI();
      player.seek(0);
    });
    list.push({ id, b, canvas, label, ctx: canvas.getContext("2d", { alpha: false }), bufs: buffers() });
  }
  const resolved = (c) => (c.id === "auto" ? Templates.auto(reel.kind) : c.id);

  function drawCard(c, now) {
    const comp = composition(resolved(c));
    const { ctx, canvas } = c;
    if (comp) {
      renderReel(ctx, comp, (now / 1000) % comp.duration, canvas.width, canvas.height, c.bufs);
    } else if (reel.kind) {
      drawFramed(ctx, canvas.width, canvas.height, paintOriginal, c.bufs);
    } else {
      ctx.setTransform(1, 0, 0, 1, 0, 0);
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, canvas.width, canvas.height);
    }
  }

  // Animate the cards (at a low frame rate) while there's something to show.
  let raf = 0, last = 0;
  function frame(now) {
    raf = 0;
    if (document.hidden || !reel.gen) return;
    if (now - last >= 1000 / 20) {
      last = now;
      for (const c of list) if (!c.b.hidden) drawCard(c, now);
    }
    raf = requestAnimationFrame(frame);
  }
  document.addEventListener("visibilitychange", () => update());

  function update() {
    for (const c of list) {
      c.b.hidden = !!reel.kind && c.id !== "auto" && !Templates.LIST[c.id].kinds.includes(reel.kind);
      c.b.classList.toggle("on", c.id === reel.template);
      c.label.textContent = c.id === "auto"
        ? "Automático" + (reel.kind ? ` (${Templates.LIST[Templates.auto(reel.kind)].name})` : "")
        : Templates.LIST[c.id].name;
      if (!c.b.hidden) drawCard(c, performance.now());
    }
    if (reel.gen && !raf) raf = requestAnimationFrame(frame);
  }
  return { update };
})();

// ---------- duration ----------

// Videos: the duration chooses the portion's length (keeping its start).
function applyDuration() {
  if (reel.kind !== "video") return;
  const dur = reel.video.info.duration;
  const len = clamp(reelSeconds() / portionFactor(), 0.5, Math.min(MAX_PORTION, dur));
  const start = clamp(reel.portion.start, 0, dur - len);
  setPortion(start, start + len);
}

$("durPreset").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  reel.duration.preset = b.dataset.value;
  reel.touched.add("duration");
  applyDuration();
  refreshUI();
  player.seek(0);
});
$("durFree").addEventListener("input", () => {
  reel.duration.free = +$("durFree").value;
  reel.touched.add("duration");
  applyDuration();
  refreshUI();
  player.draw();
});

// ---------- template base configuration ----------

const PRESETS = ["10", "15", "30", "60"];

// Load the chosen template's base configuration into the controls, except
// what was changed by hand (force: everything, back to the base).
function applyBase(force = false) {
  if (!reel.kind) return;
  const id = templateId(), base = Templates.LIST[id].base;
  if (force) reel.touched.clear();
  const touched = reel.touched;
  if (!touched.has("duration")) {
    const v = String(base.duration);
    reel.duration = PRESETS.includes(v) ? { ...reel.duration, preset: v } : { preset: "free", free: base.duration };
    $("durFree").value = reel.duration.free;
    applyDuration();
  }
  let changed = false;
  if (!touched.has("shapes") && +$("shapes").value !== base.shapes) {
    $("shapes").value = base.shapes;
    changed = true;
  }
  if (!touched.has("mode") && params().mode !== base.mode) {
    for (const x of $("mode").children) x.classList.toggle("on", +x.dataset.value === base.mode);
    changed = true;
  }
  if (!touched.has("build")) {
    $("recOrder").value = base.build.order;
    $("recCurve").value = base.build.curve;
    $("recEffect").value = base.build.effect;
    $("recEffectTime").value = base.build.effectTime;
    for (const sel of ["recOrder", "recCurve", "recEffect"]) $(sel).dispatchEvent(new Event("change"));
    Picker.refresh();
  }
  if (!touched.has("phrases")) reel.phrases = Templates.phrasesFor(id, reel.phrases);
  reel.base = id;
  renderPhrases();
  if (changed) sendParams();
  else refreshUI();
}
$("restoreBase").addEventListener("click", () => {
  applyBase(true);
  player.seek(0);
});

// Changing these by hand keeps them when switching templates.
$("shapes").addEventListener("input", () => reel.touched.add("shapes"));
$("mode").addEventListener("click", (e) => { if (e.target.closest("button")) reel.touched.add("mode"); });
for (const id of ["recOrder", "recCurve", "recEffect", "recEffectTime"]) $(id).addEventListener("input", () => reel.touched.add("build"));

// ---------- phrases ----------

const ROLE_NAMES = { title: "Título", closing: "Cierre" };

// Length of the reel as it would play now, for the phrases' seconds.
function reelLength() {
  return composition()?.duration ?? (reel.kind === "video" ? portionLength() * portionFactor() : reelSeconds());
}

function phrasesChanged(layout) {
  if (layout) reel.touched.add("phrases");
  refreshUI();
  if (!player.playing) player.draw();
}

// One card per phrase: text, position, style, and when it shows (seconds).
function renderPhrases() {
  const box = $("phrases");
  box.replaceChildren();
  reel.phrases.forEach((p, i) => {
    const card = document.createElement("div");
    card.className = "phrase";
    const role = Object.assign(document.createElement("span"), { className: "role", textContent: ROLE_NAMES[p.role] ?? `Frase ${i + 1}` });
    const text = Object.assign(document.createElement("textarea"), { rows: 2, value: p.text, placeholder: p.role === "title" ? "Ej.: La Mona Lisa con {formas} {tipo}" : p.role === "closing" ? "Ej.: ¿Cuál hago mañana? 👇" : "Texto" });
    text.dataset.role = p.role;
    text.addEventListener("input", () => { p.text = text.value; phrasesChanged(false); });
    const select = (options, value, key) => {
      const sel = document.createElement("select");
      sel.append(...Object.entries(options).map(([v, label]) => new Option(label, v, false, v === value)));
      sel.addEventListener("input", () => { p[key] = sel.value; phrasesChanged(true); });
      return sel;
    };
    const del = Object.assign(document.createElement("button"), { type: "button", className: "del", textContent: "✕", title: "Quitar" });
    del.addEventListener("click", () => {
      reel.phrases.splice(i, 1);
      renderPhrases();
      phrasesChanged(true);
    });
    const meta = document.createElement("div");
    meta.className = "meta";
    meta.append(select(Templates.POSITIONS, p.pos, "pos"), select(Templates.STYLES, p.style, "style"), del);
    const times = document.createElement("div");
    times.className = "times";
    for (const [key, label] of [["from", "desde"], ["to", "hasta"]]) {
      const input = Object.assign(document.createElement("input"), { type: "number", min: 0, step: 0.1 });
      input.dataset.key = key;
      input.addEventListener("input", () => {
        p[key] = clamp(+input.value / reelLength(), 0, 1);
        if (key === "to") delete p.extend; // an explicit end
        phrasesChanged(true);
      });
      times.append(Object.assign(document.createElement("span"), { textContent: label }), input);
    }
    card.append(role, text, meta, times);
    card._phrase = p;
    box.append(card);
  });
  syncPhraseTimes();
}

// Seconds shown for each phrase (they follow the duration).
function syncPhraseTimes() {
  const L = reelLength();
  for (const card of $("phrases").children) {
    for (const input of card.querySelectorAll("input[type=number]")) {
      if (input === document.activeElement) continue;
      input.max = L.toFixed(1);
      input.value = (card._phrase[input.dataset.key] * L).toFixed(1);
    }
  }
}

$("addPhrase").addEventListener("click", () => {
  reel.phrases.push({ role: "extra", text: "", from: 0.3, to: 0.6, pos: "middle", style: "subtitle" });
  renderPhrases();
  phrasesChanged(true);
  $("phrases").lastElementChild.querySelector("textarea").focus();
});
$("tBrand").addEventListener("input", () => phrasesChanged(false));

// ---------- loading a source ----------

async function loadSource(file) {
  if (!file) return;
  const isVideo = file.type.startsWith("video/");
  if (!isVideo && !file.type.startsWith("image/")) {
    alert("Subí una foto o un video.");
    return;
  }
  reel.generating?.cancel();
  closeLive();
  player.pause();
  if (reel.url) URL.revokeObjectURL(reel.url);
  Object.assign(reel, { kind: null, name: file.name, url: URL.createObjectURL(file), img: null, video: null, gen: null, shown: false });
  reel.framing = { mode: reel.framing.mode, zoom: 1, px: 0.5, py: 0.5 };
  $("stats").textContent = isVideo ? "Subiendo el video…" : "Cargando la foto…";
  setProgress(null, "");
  try {
    if (isVideo) {
      const form = new FormData();
      form.append("video", file);
      const d = await api("/api/videos", { method: "POST", body: form });
      srcVideo.src = reel.url;
      await new Promise((res, rej) => {
        srcVideo.onloadedmetadata = res;
        srcVideo.onerror = () => rej(new Error("el navegador no puede reproducir este video"));
      });
      reel.video = d;
      reel.kind = "video";
      for (const id of ["pStart", "pEnd"]) $(id).max = d.info.duration.toFixed(2);
      reel.portion = { start: 0, end: 0 };
      applyDuration();
    } else {
      const img = new Image();
      img.src = reel.url;
      await img.decode();
      reel.img = img;
      reel.kind = "image";
    }
  } catch (e) {
    $("stats").textContent = "Error: " + e.message;
    return;
  }
  // Default sound: the video's own, if it has one.
  if (reel.audio.mode !== "file") reel.audio.mode = reel.video?.info.hasAudio ? "source" : "none";
  const [w, h] = sourceSize();
  $("sourceInfo").textContent = `${file.name} · ${w}×${h}` + (reel.video ? ` · ${fmtTime(reel.video.info.duration)}` + (reel.video.info.hasAudio ? " · con sonido" : "") : "");
  if (reel.video) {
    fillKFPS(reel.video.info.fps || 30);
    $("testFrame").value = (reel.portion.start + reel.portion.end) / 2; // as the editor: a frame from the middle
  }
  closeLive();
  applyBase();
  setView("shapes");
  syncFramingInputs();
  refreshUI();
  player.seek(0);
  startLive();
}

function fillKFPS(fps) {
  const keep = +$("kfps").value || 8;
  const opts = [4, 6, 8, 10, 12, 15, 24, 30].filter((f) => f <= fps + 0.5);
  $("kfps").replaceChildren(...opts.map((f) => new Option(String(f), f)));
  $("kfps").value = String(opts.includes(keep) ? keep : opts.at(-1) ?? 4);
}

$("file").addEventListener("change", (e) => {
  loadSource(e.target.files[0]);
  e.target.value = "";
});
const phone = $("phone");
phone.addEventListener("dragover", (e) => { e.preventDefault(); phone.classList.add("dragging"); });
phone.addEventListener("dragleave", () => phone.classList.remove("dragging"));
phone.addEventListener("drop", (e) => {
  e.preventDefault();
  phone.classList.remove("dragging");
  loadSource(e.dataTransfer.files[0]);
});
$("dropHint").addEventListener("click", () => $("file").click());

// ---------- portion of the video ----------

function setPortion(s, e) {
  reel.portion = { start: Math.round(s * 100) / 100, end: Math.round(e * 100) / 100 };
  $("pStart").value = reel.portion.start;
  $("pEnd").value = reel.portion.end;
  // The test frame stays inside the portion (in the middle when it was out).
  const tf = $("testFrame"), old = +tf.value;
  tf.min = reel.portion.start;
  tf.max = reel.portion.end;
  if (old < reel.portion.start || old > reel.portion.end) tf.value = (reel.portion.start + reel.portion.end) / 2;
  if (+tf.value !== old) restartLiveSoon();
}

$("testFrame").addEventListener("input", () => {
  // Show it while choosing; the shapes follow once the hand stops.
  setView("original");
  player.pause();
  player.seek(testTime() - reel.portion.start);
  refreshUI();
  restartLiveSoon();
});

function onPortion(changed) {
  const dur = reel.video.info.duration;
  let s = +$("pStart").value, e = +$("pEnd").value;
  // Keep at least half a second and at most MAX_PORTION, moving the other end.
  if (changed === "start") {
    s = clamp(s, 0, dur - 0.5);
    e = clamp(e, s + 0.5, Math.min(dur, s + MAX_PORTION));
  } else {
    e = clamp(e, 0.5, dur);
    s = clamp(s, Math.max(0, e - MAX_PORTION), e - 0.5);
  }
  setPortion(s, e);
  // Choosing the portion by hand sets a free duration.
  reel.duration = { preset: "free", free: Math.round((e - s) * portionFactor() * 10) / 10 };
  reel.touched.add("duration");
  $("durFree").value = reel.duration.free;
  // Show the frame being chosen.
  setView("original");
  player.pause();
  player.seek(changed === "start" ? 0 : e - s);
  refreshUI();
}
$("pStart").addEventListener("input", () => onPortion("start"));
$("pEnd").addEventListener("input", () => onPortion("end"));

// ---------- framing controls ----------

function syncFramingInputs() {
  $("zoom").value = reel.framing.zoom;
  $("panX").value = reel.framing.px;
  $("panY").value = reel.framing.py;
  for (const b of $("fitMode").children) b.classList.toggle("on", b.dataset.value === reel.framing.mode);
}

function framingChanged() {
  refreshUI();
  player.draw();
  restartLiveSoon();
}

$("fitMode").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  reel.framing.mode = b.dataset.value;
  syncFramingInputs();
  framingChanged();
});
for (const [id, key] of [["zoom", "zoom"], ["panX", "px"], ["panY", "py"]]) {
  $(id).addEventListener("input", () => {
    reel.framing[key] = +$(id).value;
    framingChanged();
  });
}

// Drag the preview to move the framing; wheel to zoom.
(() => {
  const canvas = $("preview");
  let drag = null;
  canvas.addEventListener("pointerdown", (e) => {
    if (reel.framing.mode !== "fill" || !reel.kind) return;
    drag = { x: e.clientX, y: e.clientY };
    canvas.setPointerCapture(e.pointerId);
    canvas.classList.add("panning");
  });
  canvas.addEventListener("pointermove", (e) => {
    if (!drag) return;
    const [w, h] = sourceSize();
    const c = crop();
    // The content follows the pointer: the crop moves the other way.
    const dx = ((e.clientX - drag.x) / canvas.clientWidth) * c.w * w;
    const dy = ((e.clientY - drag.y) / canvas.clientHeight) * c.h * h;
    const freeX = w * (1 - c.w), freeY = h * (1 - c.h);
    if (freeX > 0.5) reel.framing.px = clamp(reel.framing.px - dx / freeX, 0, 1);
    if (freeY > 0.5) reel.framing.py = clamp(reel.framing.py - dy / freeY, 0, 1);
    drag = { x: e.clientX, y: e.clientY };
    syncFramingInputs();
    framingChanged();
  });
  const end = () => { drag = null; canvas.classList.remove("panning"); };
  canvas.addEventListener("pointerup", end);
  canvas.addEventListener("pointercancel", end);
  canvas.addEventListener("wheel", (e) => {
    if (reel.framing.mode !== "fill" || !reel.kind) return;
    e.preventDefault();
    reel.framing.zoom = clamp(reel.framing.zoom * Math.exp(-e.deltaY * 0.001), 1, 3);
    syncFramingInputs();
    framingChanged();
  }, { passive: false });
})();

// ---------- shape controls ----------

$("mode").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  for (const x of $("mode").children) x.classList.toggle("on", x === b);
  sendParams();
});
for (const id of ["shapes", "alpha", "alphaAuto", "repeat", "inputSize", "background", "bgAuto"]) {
  $(id).addEventListener("input", sendParams);
}
// Settings that apply at once, without finding shapes again.
for (const id of ["kfps", "adapt", "videoFlow", "interp", "recOrder", "recCurve", "recEffect", "recEffectTime"]) {
  $(id).addEventListener("input", () => {
    refreshUI();
    if (!player.playing) player.draw();
  });
}

// The recording options with their visual previews, as in the editor.
(() => {
  const fill = (id, entries) => $(id).replaceChildren(...entries.map(([v, label]) => new Option(label, v)));
  fill("recOrder", Object.entries(Build.ORDERS));
  fill("recCurve", Object.entries(Build.CURVES).map(([k, c]) => [k, c.label]));
  fill("recEffect", Object.entries(Build.EFFECTS));
  $("recCurve").value = "outCubic";
  $("recEffect").value = "fade";
  // Previews use the current shapes: short loops, so each card tells its story fast.
  const thumb = () => reel.gen?.image ?? (live.img && reel.kind === "image" ? { ...live.img, shapes: live.img.shapes.filter(Boolean) } : null);
  const quick = () => ({ direction: "build", ...buildOpts(), duration: 2.2, holdStart: 0.2, holdEnd: 0.8, effectTime: Math.min(+$("recEffectTime").value, 0.6) });
  const variant = (key) => (value) => (thumb()?.shapes.length ? Build.plan(thumb(), { ...quick(), [key]: value }) : null);
  Picker.attach($("recOrder"), variant("order"));
  // Curves: the plot of the curve, or the build playing with it.
  const curveView = Picker.curveToggle($("curveView"), "result");
  Picker.attach($("recCurve"), (value) => {
    if (curveView() === "result") return variant("curve")(value);
    if (value !== "impact") return Picker.curve(Build.CURVES[value].f);
    return thumb()?.shapes.length ? Picker.curve(Build.plan(thumb(), { ...quick(), curve: "impact" }).ease) : null;
  });
  Picker.attach($("recEffect"), variant("effect"));
  for (const id of ["recOrder", "recCurve", "recEffect", "recEffectTime"]) $(id).addEventListener("input", () => Picker.refresh());
})();

// ---------- state shown in the panel ----------

function refreshUI() {
  const has = !!reel.kind;
  $("controls").disabled = !has;
  $("dropHint").hidden = has;
  $("portion").hidden = reel.kind !== "video";
  $("fillControls").hidden = reel.framing.mode !== "fill";
  $("zoomOut").textContent = (+reel.framing.zoom).toFixed(2) + "×";
  $("shapesOut").textContent = $("shapes").value;
  $("inputSizeOut").textContent = $("inputSize").value + " px";
  $("alphaOut").textContent = $("alphaAuto").checked ? "auto" : $("alpha").value;
  $("alpha").disabled = $("alphaAuto").checked;
  $("repeatOut").textContent = $("repeat").value;
  $("background").disabled = $("bgAuto").checked;
  $("adaptOut").textContent = $("adapt").value;
  $("recEffectTimeOut").textContent = (+$("recEffectTime").value).toFixed(2) + " s";
  $("recEffectTime").disabled = $("recEffect").value === "none";
  $("photoAnim").hidden = reel.kind === "video";
  $("videoAnim").hidden = reel.kind !== "video";
  $("testFrameBox").hidden = reel.kind !== "video";
  if (reel.kind === "video") $("testFrameOut").textContent = fmtTime(testTime());
  drawOverlay();

  // The live session, as in the editor.
  const target = +$("shapes").value;
  $("pause").textContent = live.paused ? "Continuar" : "Pausar";
  if (has) {
    const n = Math.min(live.count, target);
    $("liveStatus").textContent = !live.id ? "Preparando…"
      : `Formas: ${n} / ${target} · ` + (live.paused ? "en pausa" : live.running ? "buscando…" : n >= target ? "listas" : "detenido") +
        (reel.kind === "image" && reel.gen && !stale() ? " · el reel ya las usa" : "");
  }

  // Template and duration.
  const tpl = Templates.LIST[templateId()];
  $("tplInfo").textContent = has ? tpl.desc : "Subí una fuente para ver los templates.";
  $("restoreBase").hidden = !has || reel.touched.size === 0;
  syncPhraseTimes();
  for (const b of $("durPreset").children) b.classList.toggle("on", b.dataset.value === reel.duration.preset);
  $("durFreeBox").hidden = reel.duration.preset !== "free";
  if (reel.kind === "video") {
    const { start, end } = reel.portion;
    const dur = reel.video.info.duration;
    const len = end - start, total = len * portionFactor();
    $("portionOut").textContent = `${fmtTime(start)} – ${fmtTime(end)} (${len.toFixed(1)} s)`;
    $("portionSel").style.left = (100 * start) / dur + "%";
    $("portionSel").style.width = (100 * len) / dur + "%";
    $("durInfo").textContent = `El reel dura ${total.toFixed(1)} s` +
      (portionFactor() > 1 ? ` (${len.toFixed(1)} s del video, de ida y de vuelta)` : "") +
      (total < reelSeconds() - 0.05 ? ". El video no alcanza para más." : ".");
  } else {
    $("durInfo").textContent = has ? `El reel dura ${reelSeconds()} s; los tiempos del template se ajustan solos.` : "";
  }

  // Audio and export.
  const canSource = !!reel.video?.info.hasAudio;
  if (reel.audio.mode === "source" && !canSource) reel.audio.mode = "none";
  for (const b of $("audioMode").children) {
    b.classList.toggle("on", b.dataset.value === reel.audio.mode);
    if (b.dataset.value === "source") b.disabled = !canSource;
  }
  $("audioFileBox").hidden = reel.audio.mode !== "file";
  $("audioLevels").hidden = reel.audio.mode === "none";
  $("volumeOut").textContent = Math.round(+$("volume").value * 100) + " %";
  $("fadeOutOut").textContent = (+$("fadeOut").value).toFixed(1) + " s";

  const comp = composition();
  if (!exporting) {
    $("export").disabled = !comp;
    if (has && !comp) $("expStatus").textContent = reel.kind === "image" ? "Esperá a que estén todas las formas para exportar." : "Calculá el video para poder exportar.";
    else if (has && !$("expStatus").dataset.done) $("expStatus").textContent = `Reel de ${comp.duration.toFixed(1)} s · ${Templates.LIST[comp.id].name} · 1080×1920, 30 fps.`;
  }
  $("viewMode").querySelector('[data-value="reel"]').disabled = !comp;
  // While the shapes are found again the reel can't show: back to it when ready.
  if (view === "reel" && !comp) {
    setView("shapes");
    reel.backToReel = true;
  } else if (reel.backToReel && comp && view === "shapes") {
    reel.backToReel = false;
    setView("reel");
  }
  if (!reel.generating && reel.kind === "video") {
    if (stale()) $("genStatus").innerHTML = '<span class="stale">Cambiaste el encuadre, el tramo o los ajustes: calculá el video de nuevo para ver el reel.</span>';
    else if (!reel.gen) $("genStatus").textContent = "Cuando te guste cómo queda en los frames de prueba, calculá el video.";
  }
  if (has) {
    const c = crop();
    $("stats").textContent = `${reel.kind === "image" ? "Foto" : "Video"} · ${tpl.name} · ` +
      (c ? `recorte ${Math.round(c.w * 100)}% × ${Math.round(c.h * 100)}% del original` : "completa, con fondo desenfocado");
  }
  cards.update();
}


// ---------- audio ----------

// Where the sound comes from: { url (for the preview), start }; null if none.
function audioSource() {
  const a = reel.audio;
  if (a.mode === "file" && a.url) return { url: a.url, start: Math.max(0, +$("audioStart").value || 0) };
  if (a.mode === "source" && reel.video?.info.hasAudio) return { url: reel.url, start: reel.portion.start };
  return null;
}

$("audioMode").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b || b.disabled) return;
  reel.audio.mode = b.dataset.value;
  refreshUI();
  player.seek(player.time);
});
$("audioFile").addEventListener("change", (e) => {
  const f = e.target.files[0];
  if (!f) return;
  if (reel.audio.url) URL.revokeObjectURL(reel.audio.url);
  Object.assign(reel.audio, { mode: "file", file: f, url: URL.createObjectURL(f) });
  $("audioName").textContent = f.name;
  e.target.value = "";
  refreshUI();
});
for (const id of ["volume", "fadeOut", "audioStart"]) $(id).addEventListener("input", refreshUI);

// ---------- export ----------

let exporting = null; // AbortController while exporting

// Settings of the reel, stored with it to compare later what works best.
function reelMeta(comp, res, seconds) {
  const a = reel.audio;
  return {
    v: 1,
    template: comp.id, templateChoice: reel.template,
    duration: Math.round(comp.duration * 100) / 100, durationChoice: { ...reel.duration },
    source: { kind: reel.kind, name: reel.name, size: sourceSize(), portion: reel.kind === "video" ? { ...reel.portion } : null },
    framing: { ...reel.framing }, crop: crop(),
    shapes: params(),
    animation: reel.kind === "image" ? buildOpts() : { ...videoOpts(), interp: $("interp").value },
    base: reel.base, changedByHand: [...reel.touched],
    phrases: reel.phrases.filter((p) => p.text.trim()).map(({ role, text, from, to, pos, style }) => ({ role, text, from, to, pos, style })),
    brand: $("tBrand").value,
    audio: { mode: a.mode, name: a.mode === "file" ? a.file?.name : null, start: audioSource()?.start ?? 0, volume: +$("volume").value, fadeOut: +$("fadeOut").value },
    export: { width: res.width, height: res.height, fps: 30, codec: res.codec, seconds: Math.round(seconds * 10) / 10 },
  };
}

// Before drawing frame t, have the hidden <video> on the right frame if the
// original is visible then.
async function seekSourceFor(comp, t) {
  if (reel.kind !== "video" || t < comp.originalFrom) return;
  const vt = comp.videoTime(t);
  if (vt === null) return;
  const want = reel.portion.start + Math.min(vt, portionLength() - 0.04);
  if (Math.abs(srcVideo.currentTime - want) < 0.001 && srcVideo.readyState >= 2) return;
  await new Promise((resolve) => {
    const done = () => { clearTimeout(timer); srcVideo.removeEventListener("seeked", done); resolve(); };
    const timer = setTimeout(done, 3000);
    srcVideo.addEventListener("seeked", done);
    srcVideo.currentTime = want;
  });
}

function setExport(fraction, text) {
  $("expProgress").hidden = fraction === null;
  if (fraction !== null) $("expProgress").value = fraction;
  $("expStatus").textContent = text;
}

$("export").addEventListener("click", async () => {
  if (exporting) {
    exporting.abort();
    return;
  }
  const comp = composition();
  if (!comp) return;
  player.pause();
  srcVideo.pause();
  exporting = new AbortController();
  $("export").textContent = "Cancelar";
  $("export").disabled = false;
  delete $("expStatus").dataset.done;
  const started = performance.now();
  try {
    const bufs = buffers();
    const res = await Motion.encodeMP4({
      width: 1080, height: 1920, duration: comp.duration,
      prepare: (t) => seekSourceFor(comp, t),
      render: (ctx, t, w, h) => renderReel(ctx, comp, t, w, h, bufs),
    }, {
      fps: 30, longSide: 1920, signal: exporting.signal,
      onProgress: (f) => setExport(f * 0.9, `Codificando: ${Math.round(f * 100)} %`),
    });
    const seconds = (performance.now() - started) / 1000;
    setExport(0.92, "Agregando el audio y guardando…");
    const form = new FormData();
    form.append("video", res.blob, "reel.mp4");
    const a = reel.audio, src = audioSource();
    if (src && a.mode === "file") {
      form.append("audio", a.file);
      form.append("audioStart", src.start);
    } else if (src && a.mode === "source") {
      form.append("source", reel.video.id);
      form.append("sourceStart", src.start);
    }
    form.append("volume", $("volume").value);
    form.append("fadeOut", $("fadeOut").value);
    form.append("save", "1");
    form.append("meta", JSON.stringify(reelMeta(comp, res, seconds)));
    const entry = await api("/api/reels/mux", { method: "POST", body: form, signal: exporting.signal });
    const link = document.createElement("a");
    link.href = `/api/reels/${entry.id}/video?download=1`;
    link.download = `reel-${entry.id}.mp4`;
    document.body.append(link);
    link.click();
    link.remove();
    setExport(null, `Listo en ${((performance.now() - started) / 1000).toFixed(1)} s: ${(entry.size / 1e6).toFixed(1)} MB, descargado y guardado en la biblioteca.`);
    $("expStatus").dataset.done = "1";
    loadLibrary();
  } catch (e) {
    setExport(null, e.name === "AbortError" ? "Exportación cancelada." : "Error: " + e.message);
    $("expStatus").dataset.done = "1";
  } finally {
    exporting = null;
    $("export").textContent = "Exportar reel (MP4 1080×1920)";
    refreshUI();
  }
});

// ---------- library ----------

async function loadLibrary() {
  const box = $("library");
  let list;
  try {
    list = await api("/api/reels");
  } catch (e) {
    box.innerHTML = "";
    box.append(Object.assign(document.createElement("p"), { className: "hint", textContent: "No se pudo leer la biblioteca: " + e.message }));
    return;
  }
  box.innerHTML = "";
  if (!list.length) {
    box.append(Object.assign(document.createElement("p"), { className: "hint", textContent: "Todavía no exportaste reels." }));
    return;
  }
  for (const e of list) {
    const m = e.meta || {};
    const row = document.createElement("div");
    row.className = "reel-item";
    const v = document.createElement("video");
    v.src = `/api/reels/${e.id}/video#t=${(e.duration * 0.85).toFixed(1)}`; // a frame near the end
    v.preload = "metadata";
    v.muted = true;
    v.title = "Ver";
    v.addEventListener("click", () => window.open(v.src, "_blank"));
    const info = document.createElement("div");
    info.className = "info";
    const title = document.createElement("strong");
    title.textContent = (Templates.LIST[m.template]?.name ?? "Reel") + " · " + fmtTime(e.duration);
    const sub = document.createElement("span");
    sub.textContent = new Date(e.created).toLocaleString() + " · " + (e.size / 1e6).toFixed(1) + " MB" + (m.texts?.title ? " · " + m.texts.title : "");
    info.append(title, sub);
    const actions = document.createElement("div");
    actions.className = "actions";
    const dl = Object.assign(document.createElement("a"), { className: "button", href: `/api/reels/${e.id}/video?download=1`, textContent: "Descargar" });
    const del = Object.assign(document.createElement("button"), { type: "button", textContent: "Borrar" });
    del.addEventListener("click", async () => {
      if (!confirm("¿Borrar este reel de la biblioteca?")) return;
      await api(`/api/reels/${e.id}`, { method: "DELETE" }).catch((err) => alert(err.message));
      loadLibrary();
    });
    actions.append(dl, del);
    row.append(v, info, actions);
    box.append(row);
  }
}

refreshUI();
player.draw();
loadLibrary();
