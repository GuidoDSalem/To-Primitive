"use strict";

// Reel editor: a photo or a portion of a video, framed vertically (9:16)
// and turned into shapes. Templates, texts and audio build on top of this.

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

// Until templates exist, photos animate with a plain build.
const DEFAULT_BUILD = {
  direction: "build", order: "original", curve: "outCubic", effect: "fade", effectTime: 0.5,
  duration: 5, holdStart: 0.3, holdEnd: 1.5,
};

const reel = {
  kind: null,       // "image" | "video"
  name: "",
  url: null,        // object URL of the local file
  img: null,        // the photo
  video: null,      // { id, info } of the uploaded video
  portion: { start: 0, end: 0 },
  framing: { mode: "fill", zoom: 1, px: 0.5, py: 0.5 },
  gen: null,        // generated shapes: { key, plan } (photo) or { key, anim } (video)
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

const layer = document.createElement("canvas");
const tiny = document.createElement("canvas");

// Draw a frame of the reel: paint(ctx, w, h) draws the content filling w x h.
// In "fit" mode the content is letterboxed over a blurred, darkened copy of
// itself (scaling a tiny copy up blurs it for free).
function drawFramed(ctx, W, H, paint) {
  ctx.setTransform(1, 0, 0, 1, 0, 0);
  if (reel.framing.mode === "fill") {
    paint(ctx, W, H);
    return;
  }
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
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(tiny, (W - lw * s) / 2, (H - lh * s) / 2, lw * s, lh * s);
  ctx.fillStyle = "rgba(0,0,0,0.35)";
  ctx.fillRect(0, 0, W, H);
  ctx.drawImage(layer, Math.round((W - lw) / 2), Math.round((H - lh) / 2));
}

// The original (photo, or the video's current frame) with the current framing.
function paintOriginal(ctx, w, h) {
  const el = reel.kind === "image" ? reel.img : srcVideo;
  const [sw, sh] = sourceSize();
  const c = crop() ?? { x: 0, y: 0, w: 1, h: 1 };
  ctx.fillStyle = "#000";
  ctx.fillRect(0, 0, w, h);
  if (sw && (reel.kind === "image" || srcVideo.readyState >= 2)) {
    ctx.drawImage(el, c.x * sw, c.y * sh, c.w * sw, c.h * sh, 0, 0, w, h);
  }
}

function paintShapes(ctx, w, h, t) {
  if (reel.gen.plan) reel.gen.plan.render(ctx, t, w, h);
  else Motion.render(ctx, reel.gen.anim, t, "smooth", w, h);
}

// ---------- generation ----------

function params() {
  return {
    shapes: +$("shapes").value,
    mode: +document.querySelector("#mode .on").dataset.value,
    alpha: 128, repeat: 0,
    inputSize: +$("inputSize").value,
    background: "",
  };
}

// Everything the shapes depend on: when it changes, they must be regenerated.
function genKey() {
  return JSON.stringify({
    kind: reel.kind, mode: reel.framing.mode, crop: crop(), params: params(),
    portion: reel.kind === "video" ? reel.portion : null,
  });
}
const stale = () => !!reel.gen && reel.gen.key !== genKey();

function setProgress(fraction, text) {
  $("genProgress").hidden = fraction === null;
  if (fraction !== null) $("genProgress").value = fraction;
  $("genStatus").textContent = text;
}

// Photo: crop it as framed and let an image session find the shapes.
async function generateImage(p) {
  const [w, h] = sourceSize();
  const c = crop() ?? { x: 0, y: 0, w: 1, h: 1 };
  const sw = c.w * w, sh = c.h * h;
  const scale = Math.min(1, 1024 / Math.max(sw, sh));
  const cv = document.createElement("canvas");
  cv.width = Math.max(1, Math.round(sw * scale));
  cv.height = Math.max(1, Math.round(sh * scale));
  cv.getContext("2d").drawImage(reel.img, c.x * w, c.y * h, sw, sh, 0, 0, cv.width, cv.height);
  const blob = await new Promise((r) => cv.toBlob(r, "image/jpeg", 0.92));
  const form = new FormData();
  form.append("image", blob, "reel.jpg");
  form.append("params", JSON.stringify(p));
  const d = await api("/api/sessions", { method: "POST", body: form });

  return new Promise((resolve, reject) => {
    const es = new EventSource(`/api/sessions/${d.id}/events`);
    const img = { width: 0, height: 0, background: "#000", shapes: [], scores: [], baseScore: 1 };
    let gen = null;
    const count = () => img.shapes.filter(Boolean).length;
    const progress = () => setProgress(Math.min(1, count() / p.shapes), `Buscando formas: ${Math.min(count(), p.shapes)} / ${p.shapes}`);
    const check = (running, n) => {
      if (running || n < p.shapes) return;
      es.close();
      resolve({ ...img, shapes: img.shapes.slice(0, p.shapes), scores: img.scores.slice(0, p.shapes) });
    };
    es.addEventListener("snapshot", (e) => {
      const s = JSON.parse(e.data);
      gen = s.g;
      Object.assign(img, { width: s.width, height: s.height, background: s.background, baseScore: s.baseScore });
      img.shapes = s.data.slice();
      img.scores = s.scores.slice();
      progress();
      check(s.running, s.data.length);
    });
    es.addEventListener("shape", (e) => {
      const s = JSON.parse(e.data);
      if (s.g !== gen) return;
      img.shapes[s.i] = s.data;
      img.scores[s.i] = s.score;
      progress();
    });
    es.addEventListener("status", (e) => {
      const s = JSON.parse(e.data);
      if (s.g === gen) check(s.running, s.count);
    });
    reel.generating = { cancel: () => { es.close(); reject(new DOMException("cancelado", "AbortError")); } };
  });
}

// Video: compute keyframes for the chosen portion and framing.
async function generateVideo(p) {
  const v = reel.video;
  const body = {
    ...p, kfps: 8, adapt: 60, flow: true,
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
  const p = params();
  $("generate").textContent = "Cancelar";
  setProgress(0, "Preparando…");
  const started = performance.now();
  try {
    if (reel.kind === "image") {
      const image = await generateImage(p);
      reel.gen = { key, plan: Build.plan(image, DEFAULT_BUILD), image };
    } else {
      reel.gen = { key, anim: await generateVideo(p) };
    }
    setProgress(null, `Listo en ${((performance.now() - started) / 1000).toFixed(1)} s.`);
    setView("shapes");
    player.seek(0);
    player.play();
  } catch (e) {
    setProgress(null, e.name === "AbortError" ? "Cancelado." : "Error: " + e.message);
  } finally {
    reel.generating = null;
    $("generate").textContent = "Generar formas";
    refreshUI();
  }
});

// ---------- preview player ----------

let view = "original"; // what the preview shows: "shapes" or "original"

function setView(v) {
  view = v;
  for (const b of $("viewMode").children) b.classList.toggle("on", b.dataset.value === v);
  player.draw();
}
$("viewMode").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (b && !b.disabled) setView(b.dataset.value);
});

const showingShapes = () => view === "shapes" && reel.gen && !stale();

function reelDuration() {
  if (reel.kind === "video") return Math.max(0.1, reel.portion.end - reel.portion.start);
  if (reel.gen?.plan) return reel.gen.plan.duration;
  return 5;
}

const player = (() => {
  const canvas = $("preview");
  const ctx = canvas.getContext("2d", { alpha: false });
  let t = 0, playing = false, last = 0, raf = 0;

  // Keep the hidden <video> on the frame for time t (original view).
  function syncVideo(play) {
    if (reel.kind !== "video") return;
    const want = reel.portion.start + t;
    if (play) {
      if (Math.abs(srcVideo.currentTime - want) > 0.25) srcVideo.currentTime = want;
      srcVideo.play().catch(() => {});
    } else {
      srcVideo.pause();
      if (Math.abs(srcVideo.currentTime - want) > 0.01) srcVideo.currentTime = want;
    }
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
    if (!reel.kind) {
      ctx.fillStyle = "#000";
      ctx.fillRect(0, 0, w, h);
    } else if (showingShapes()) {
      drawFramed(ctx, w, h, (c, cw, ch) => paintShapes(c, cw, ch, t));
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
    const dur = reelDuration();
    if (reel.kind === "video" && !showingShapes() && !srcVideo.paused && srcVideo.readyState >= 2) {
      t = srcVideo.currentTime - reel.portion.start;
    } else {
      t += (now - last) / 1000;
    }
    last = now;
    if (t >= dur || t < 0) {
      t = 0;
      syncVideo(true);
    }
    draw();
    raf = requestAnimationFrame(tick);
  }

  function play() {
    if (!reel.kind) return;
    playing = true;
    $("play").textContent = "❚❚";
    last = performance.now();
    syncVideo(true);
    if (!raf) raf = requestAnimationFrame(tick);
  }

  function pause() {
    playing = false;
    $("play").textContent = "▶";
    syncVideo(false);
    draw();
  }

  function seek(x) {
    t = clamp(x, 0, reelDuration());
    syncVideo(playing);
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
  return { draw, play, pause, seek, get playing() { return playing; } };
})();

// Areas the apps cover with their own buttons and captions (TikTok, Reels,
// Shorts): keep titles and logos out of them.
const SAFE = { top: 0.13, bottom: 0.2, right: 0.12, rightFrom: 0.45 };

function drawOverlay() {
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

// ---------- loading a source ----------

async function loadSource(file) {
  if (!file) return;
  const isVideo = file.type.startsWith("video/");
  if (!isVideo && !file.type.startsWith("image/")) {
    alert("Subí una foto o un video.");
    return;
  }
  reel.generating?.cancel();
  player.pause();
  if (reel.url) URL.revokeObjectURL(reel.url);
  Object.assign(reel, { kind: null, name: file.name, url: URL.createObjectURL(file), img: null, video: null, gen: null });
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
      const dur = d.info.duration;
      reel.portion = { start: 0, end: Math.min(dur, 10) };
      for (const id of ["pStart", "pEnd"]) $(id).max = dur.toFixed(2);
      $("pStart").value = 0;
      $("pEnd").value = reel.portion.end;
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
  const [w, h] = sourceSize();
  $("sourceInfo").textContent = `${file.name} · ${w}×${h}` + (reel.video ? ` · ${fmtTime(reel.video.info.duration)}` + (reel.video.info.hasAudio ? " · con sonido" : "") : "");
  setView("original");
  syncFramingInputs();
  refreshUI();
  player.seek(0);
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
  $("pStart").value = s;
  $("pEnd").value = e;
  reel.portion = { start: s, end: e };
  // Show the frame being chosen.
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
  refreshUI();
});
for (const id of ["shapes", "inputSize"]) $(id).addEventListener("input", refreshUI);

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
  drawOverlay();
  if (reel.kind === "video") {
    const { start, end } = reel.portion;
    const dur = reel.video.info.duration;
    $("portionOut").textContent = `${fmtTime(start)} – ${fmtTime(end)} (${(end - start).toFixed(1)} s)`;
    $("portionSel").style.left = (100 * start) / dur + "%";
    $("portionSel").style.width = (100 * (end - start)) / dur + "%";
  }
  const shapesBtn = $("viewMode").querySelector('[data-value="shapes"]');
  shapesBtn.disabled = !reel.gen || stale();
  if (!reel.generating) {
    if (!has) $("genStatus").textContent = "";
    else if (stale()) $("genStatus").innerHTML = '<span class="stale">Cambiaste el encuadre o los ajustes: generá de nuevo para ver las formas.</span>';
    else if (!reel.gen) $("genStatus").textContent = "Elegí el encuadre y generá las formas.";
  }
  if (has) {
    const c = crop();
    $("stats").textContent = `${reel.kind === "image" ? "Foto" : "Video"} · ` +
      (c ? `recorte ${Math.round(c.w * 100)}% × ${Math.round(c.h * 100)}% del original` : "completa, con fondo desenfocado");
  }
}

refreshUI();
player.draw();
