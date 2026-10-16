"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  id: null,     // active editing session (an image, or a frame of the video)
  events: null,
  gen: 0,
  shapes: [],   // SVG fragments of the current run, in order
  data: [],     // the same shapes as geometry (see encode.go), for recordings
  scores: [],   // score after each shape (lower = closer to the image)
  baseScore: 1, // score of the empty canvas
  image: null,  // { width, height, background } of the current run
  score: 1,
  running: false,
  params: null,
  rate: { count: 0, time: 0, value: 0 },
  video: null,  // { id, info, frames: [{ t, sid }], active, events, job }
};

const api = (path, opts = {}) =>
  fetch(path, opts).then(async (res) => {
    if (!res.ok) throw new Error(await res.text());
    return res.headers.get("Content-Type")?.includes("json") ? res.json() : null;
  });
const putParams = (sid, params, extra = {}) =>
  api(`/api/sessions/${sid}/params`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...params, ...extra }),
  }).catch((e) => console.error(e));

const fmtTime = (s) => `${Math.floor(s / 60)}:${String(Math.floor(s % 60)).padStart(2, "0")}`;
const fmtClock = (s) => `${fmtTime(s)}.${Math.floor((s % 1) * 10)}`;

// ---------- params <-> controls ----------

function readParams() {
  return {
    shapes: +$("shapes").value,
    mode: +(document.querySelector("#mode .on")?.dataset.value ?? 1),
    alpha: $("alphaAuto").checked ? 0 : +$("alpha").value,
    repeat: +$("repeat").value,
    inputSize: +$("inputSize").value,
    background: $("bgAuto").checked ? "" : $("background").value,
    paused: state.params?.paused || false,
  };
}

function writeParams(p) {
  $("shapes").value = p.shapes;
  setMode(p.mode);
  $("alphaAuto").checked = p.alpha === 0;
  if (p.alpha) $("alpha").value = p.alpha;
  $("repeat").value = p.repeat;
  $("inputSize").value = p.inputSize;
  $("bgAuto").checked = !p.background;
  if (p.background) $("background").value = p.background;
  updateOutputs();
}

function setMode(v) {
  for (const b of document.querySelectorAll("#mode button")) {
    b.classList.toggle("on", +b.dataset.value === v);
  }
}

function updateOutputs() {
  $("shapesOut").textContent = $("shapes").value;
  $("alphaOut").textContent = $("alphaAuto").checked ? "auto" : $("alpha").value;
  $("alpha").disabled = $("alphaAuto").checked;
  $("repeatOut").textContent = $("repeat").value;
  $("inputSizeOut").textContent = $("inputSize").value + " px";
  $("background").disabled = $("bgAuto").checked;
  $("pause").textContent = state.params?.paused ? "Continuar" : "Pausar";
  $("adaptOut").textContent = $("adapt").value;
}

let sendTimer = null;
function sendParams(extra = {}, delay = 120) {
  if (Live.active()) {
    state.params = { ...readParams(), ...extra };
    delete state.params.restart;
    updateOutputs();
    Live.setParams(state.params, !!extra.restart);
    return;
  }
  if (!state.id) return;
  state.params = { ...readParams(), ...extra };
  delete state.params.restart;
  updateOutputs();
  scheduleRender();
  clearTimeout(sendTimer);
  const sid = state.id;
  const params = state.params;
  sendTimer = setTimeout(() => putParams(sid, params, { restart: !!extra.restart }), delay);
}

for (const id of ["shapes", "alpha", "alphaAuto", "repeat", "inputSize", "background", "bgAuto"]) {
  $(id).addEventListener("input", () => sendParams());
}
$("adapt").addEventListener("input", updateOutputs);
$("mode").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  setMode(+b.dataset.value);
  sendParams({}, 0);
});
$("pause").addEventListener("click", () => sendParams({ paused: !state.params.paused }, 0));
$("restart").addEventListener("click", () => sendParams({ restart: true }, 0));

// ---------- rendering the live session ----------

let renderQueued = false;
function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(render);
}

// Sync the DOM with the first `shapes` param shapes we have.
function render() {
  renderQueued = false;
  const g = $("shapesG");
  const visible = Math.min(state.params?.shapes ?? 0, state.shapes.length);
  while (g.childElementCount > visible) g.lastElementChild.remove();
  if (g.childElementCount < visible) {
    g.insertAdjacentHTML("beforeend", state.shapes.slice(g.childElementCount, visible).join(""));
  }
  renderStats(visible);
  if (!state.video) {
    player.changed(); // the recording uses these shapes
    Picker.refreshSoon(); // and so do the option previews
  }
}

function renderStats(visible) {
  const p = state.params;
  if (!p) return;
  const similarity = ((1 - state.score) * 100).toFixed(2);
  const status = state.running ? "Generando…" : p.paused ? "Pausado" : "Listo";
  const rate = state.running && state.rate.value ? ` · ${state.rate.value.toFixed(1)} formas/s` : "";
  $("stats").innerHTML =
    `<b>${status}</b>${rate}<br>Formas: <b>${visible}</b> / ${p.shapes}` +
    ` · Similitud: <b>${similarity}%</b>`;
}

function trackRate() {
  const now = performance.now();
  const r = state.rate;
  if (now - r.time > 1000) {
    r.value = r.time ? (state.shapes.length - r.count) / ((now - r.time) / 1000) : 0;
    r.count = state.shapes.length;
    r.time = now;
  }
}

function sizeSVG(d) {
  $("out").setAttribute("viewBox", `0 0 ${d.width} ${d.height}`);
  for (const r of [$("bg"), $("clipRect")]) {
    r.setAttribute("width", d.width);
    r.setAttribute("height", d.height);
  }
  $("bg").setAttribute("fill", d.background);
}

// ---------- session events ----------

function connect() {
  state.events?.close();
  const es = new EventSource(`/api/sessions/${state.id}/events`);
  state.events = es;

  es.addEventListener("snapshot", (e) => {
    const d = JSON.parse(e.data);
    state.gen = d.g;
    state.shapes = d.shapes;
    state.data = d.data;
    state.scores = d.scores;
    state.baseScore = d.baseScore;
    state.image = { width: d.width, height: d.height, background: d.background };
    state.score = d.score;
    state.running = d.running;
    state.rate = { count: d.shapes.length, time: 0, value: 0 };
    // Keep local control values if the user is mid-drag; adopt the server's otherwise.
    if (!state.params) {
      state.params = d.params;
      writeParams(d.params);
    }
    sizeSVG(d);
    if ($("bgAuto").checked) $("background").value = d.background;
    $("shapesG").replaceChildren();
    scheduleRender();
  });

  es.addEventListener("shape", (e) => {
    const d = JSON.parse(e.data);
    if (d.g !== state.gen) return;
    state.shapes[d.i] = d.svg;
    state.data[d.i] = d.data;
    state.scores[d.i] = d.score;
    state.score = d.score;
    trackRate();
    scheduleRender();
  });

  es.addEventListener("status", (e) => {
    const d = JSON.parse(e.data);
    if (d.g !== state.gen) return;
    state.running = d.running;
    state.score = d.score;
    if (!d.running) state.rate.value = 0;
    scheduleRender();
  });
}

// Switch the editor to session sid; originalURL is what "Ver original" shows.
function openSession(sid, originalURL) {
  // A debounced update still pending for the previous session would undo
  // the pause it just got: drop it (the pause already carries the latest params).
  clearTimeout(sendTimer);
  state.id = sid;
  state.shapes = [];
  const img = $("original");
  if (img.src.startsWith("blob:")) URL.revokeObjectURL(img.src);
  img.src = originalURL;

  $("drop").hidden = true;
  $("compare").hidden = false;
  $("controls").disabled = false;
  // Images get a "recording" tab; videos an "animation" one.
  $("tabs").hidden = false;
  $("playerTab").textContent = state.video ? "Animación" : "Grabación";
  $("recordSection").hidden = !!state.video;
  $("renderEmpty").textContent = state.video
    ? "Todavía no hay animación. Ajustá los parámetros y tocá “Calcular animación”."
    : "Esperando las formas del editor…";
  player.reset();
  showTab("editor");
  connect();
}

// ---------- upload ----------

async function upload(file) {
  if (!file) return;
  if (Live.active()) leaveLive();
  try {
    if (file.type.startsWith("video/")) await uploadVideo(file);
    else if (file.type.startsWith("image/")) await uploadImage(file);
    else alert("Formato no soportado: subí una imagen o un video.");
  } catch (e) {
    $("stats").textContent = "Error: " + e.message;
  }
}

async function uploadImage(file) {
  $("stats").textContent = "Subiendo…";
  const form = new FormData();
  form.append("image", file);
  if (state.params) form.append("params", JSON.stringify({ ...state.params, paused: false }));
  const d = await api("/api/sessions", { method: "POST", body: form });
  closeVideo();
  if (state.params) state.params.paused = false;
  openSession(d.id, URL.createObjectURL(file));
}

async function uploadVideo(file) {
  $("stats").textContent = "Subiendo video…";
  const form = new FormData();
  form.append("video", file);
  const d = await api("/api/videos", { method: "POST", body: form });
  closeVideo();
  if (state.id && state.params) putParams(state.id, state.params, { paused: true });

  const info = d.info;
  state.video = { id: d.id, info, frames: [], active: -1, job: null, anim: null };
  $("videoSection").hidden = false;
  $("renderSection").hidden = false;
  $("tabs").hidden = false;
  $("exportTitle").textContent = "Exportar frame";
  $("videoInfo").textContent =
    `${fmtTime(info.duration)} · ${+info.fps.toFixed(2)} fps · ${info.width}×${info.height}` +
    (info.hasAudio ? " · con audio" : "");
  $("audio").disabled = !info.hasAudio;
  $("timeline").max = Math.max(0, info.duration - 0.05).toFixed(2);
  $("timeline").value = (info.duration / 2).toFixed(2);
  fillKFPS(info.fps);
  resetRenderView();
  scrub();
  connectVideo();
  await addFrame(+$("timeline").value);
}

function closeVideo() {
  if (!state.video) return;
  state.video.events?.close();
  state.video = null;
  $("videoSection").hidden = true;
  $("renderSection").hidden = true;
  $("exportAnimSection").hidden = true;
  $("tabs").hidden = true;
  player.reset();
  $("frames").replaceChildren();
  $("exportTitle").textContent = "Exportar";
}

// ---------- live (webcam) ----------

async function enterLive() {
  closeVideo();
  if (state.id && state.params) putParams(state.id, state.params, { paused: true });
  state.events?.close();
  state.id = null;
  // Live needs light settings to keep up: few shapes, small working size.
  const p = readParams();
  if (p.shapes > 150) $("shapes").value = 100;
  if (p.inputSize > 160) $("inputSize").value = 128;
  state.params = { ...readParams(), paused: false };
  updateOutputs();
  for (const id of ["drop", "view", "renderView", "tabs", "compare", "recordSection", "renderSection", "exportAnimSection", "videoSection"]) $(id).hidden = true;
  $("exportTitle").parentElement.hidden = true;
  $("controls").disabled = false;
  $("stats").textContent = "Abriendo la cámara…";
  try {
    await Live.start(state.params);
  } catch (e) {
    $("stats").textContent = "No se pudo usar la cámara: " + e.message;
    $("drop").hidden = false;
    $("exportTitle").parentElement.hidden = false;
  }
}

function leaveLive() {
  Live.stop();
  $("exportTitle").parentElement.hidden = false;
  $("drop").hidden = !!state.id;
  $("stats").textContent = state.id ? "" : "Sin imagen";
}

$("liveStart").addEventListener("click", enterLive);
$("liveStop").addEventListener("click", leaveLive);

$("file").addEventListener("change", (e) => {
  upload(e.target.files[0]);
  e.target.value = "";
});

const stage = $("stage");
stage.addEventListener("dragover", (e) => { e.preventDefault(); stage.classList.add("dragging"); });
stage.addEventListener("dragleave", () => stage.classList.remove("dragging"));
stage.addEventListener("drop", (e) => {
  e.preventDefault();
  stage.classList.remove("dragging");
  upload(e.dataTransfer.files[0]);
});
$("drop").addEventListener("click", () => $("file").click());

// ---------- video: test frames ----------

const frameURL = (t, size) => `/api/videos/${state.video.id}/frame?t=${t.toFixed(2)}&size=${size}`;

let scrubTimer = null;
function scrub() {
  const t = +$("timeline").value;
  $("timeOut").textContent = fmtTime(t) + "." + String(Math.floor((t % 1) * 10));
  clearTimeout(scrubTimer);
  scrubTimer = setTimeout(() => { $("scrub").src = frameURL(t, 320); }, 120);
}
$("timeline").addEventListener("input", scrub);
$("addFrame").addEventListener("click", () => addFrame(+$("timeline").value));

async function addFrame(t) {
  const v = state.video;
  $("addFrame").disabled = true;
  try {
    const params = state.params ? { ...readParams(), paused: false } : null;
    const d = await api(`/api/videos/${v.id}/frames`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ t, params }),
    });
    if (state.video !== v) return;
    v.frames.push({ t, sid: d.id });
    v.frames.sort((a, b) => a.t - b.t);
    activateFrame(v.frames.findIndex((f) => f.sid === d.id));
  } catch (e) {
    alert("No se pudo extraer el frame: " + e.message);
  } finally {
    $("addFrame").disabled = false;
  }
}

// Make frame i the one being edited; pause the others so they don't compete
// for the CPU.
function activateFrame(i) {
  const v = state.video;
  const prev = v.frames[v.active];
  if (prev && prev.sid !== v.frames[i].sid && state.params) putParams(prev.sid, state.params, { paused: true });
  v.active = i;
  const f = v.frames[i];
  openSession(f.sid, frameURL(f.t, 1024));
  if (state.params) sendParams({ paused: false }, 0); // carry the current settings over
  drawFrames();
}

function removeFrame(i) {
  const v = state.video;
  if (v.frames.length <= 1) return;
  const [f] = v.frames.splice(i, 1);
  if (state.params) putParams(f.sid, state.params, { paused: true });
  if (v.active === i) activateFrame(Math.min(i, v.frames.length - 1));
  else {
    if (v.active > i) v.active--;
    drawFrames();
  }
}

function drawFrames() {
  const v = state.video;
  $("frames").replaceChildren(...v.frames.map((f, i) => {
    const b = document.createElement("button");
    b.className = "frame" + (i === v.active ? " on" : "");
    b.title = "Editar este frame";
    b.innerHTML = `<img src="${frameURL(f.t, 320)}" alt=""><span>${fmtTime(f.t)}</span>` +
      (v.frames.length > 1 ? `<i title="Quitar">×</i>` : "");
    b.addEventListener("click", (e) => {
      if (e.target.tagName === "I") removeFrame(i);
      else if (i !== v.active) activateFrame(i);
    });
    return b;
  }));
}

// ---------- video: computing the animation ----------

function fillKFPS(fps) {
  const opts = [4, 6, 8, 10, 12, 15, 24, 30].filter((f) => f <= fps + 0.5);
  $("kfps").replaceChildren(...opts.map((f) => new Option(String(f), f, false, f === 8)));
  if (!opts.includes(8)) $("kfps").value = String(opts.at(-1) ?? 4);
}

$("compute").addEventListener("click", async () => {
  const v = state.video;
  // Free the CPU for the computation.
  sendParams({ paused: true }, 0);
  const opts = { ...readParams(), paused: false, kfps: +$("kfps").value, adapt: +$("adapt").value, flow: $("videoFlow").checked };
  try {
    await api(`/api/videos/${v.id}/compute`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(opts),
    });
    showTab("render");
  } catch (e) {
    alert("No se pudo iniciar el cálculo: " + e.message);
  }
});
$("cancelCompute").addEventListener("click", () =>
  api(`/api/videos/${state.video.id}/compute`, { method: "DELETE" }));

function connectVideo() {
  const v = state.video;
  const es = new EventSource(`/api/videos/${v.id}/events`);
  v.events = es;
  let snapshot = true;
  es.addEventListener("job", (e) => {
    v.job = JSON.parse(e.data);
    // The first event is the snapshot: fetch whatever was computed before
    // we subscribed (later keyframes arrive as events).
    if (snapshot && v.job) syncKeyframes(v);
    snapshot = false;
    showJob(v.job);
  });
  es.addEventListener("open", () => { snapshot = true; });
  es.addEventListener("animation", (e) => {
    v.anim = { ...JSON.parse(e.data), keyframes: [] };
    player.reset();
  });
  es.addEventListener("keyframe", (e) => {
    const d = JSON.parse(e.data);
    if (!v.anim) return;
    v.anim.keyframes[d.i] = d.shapes;
    player.changed();
  });
}

async function syncKeyframes(v) {
  try {
    const a = await api(`/api/videos/${v.id}/keyframes`);
    if (state.video !== v) return;
    const kfs = v.anim?.keyframes ?? [];
    for (let i = 0; i < a.keyframes.length; i++) kfs[i] = a.keyframes[i];
    v.anim = { ...a, keyframes: kfs };
    player.changed();
  } catch { /* nothing computed yet */ }
}

function showJob(j) {
  const running = j?.state === "running";
  $("compute").hidden = running;
  $("cancelCompute").hidden = !running;
  $("progress").hidden = !j;
  $("exportAnimSection").hidden = j?.state !== "done";
  if (!j) {
    $("computeStatus").textContent = "";
    return;
  }
  $("progress").value = j.total ? j.frame / j.total : 0;
  // The first keyframe is a full search and much slower than the rest, so
  // measure the pace from the first one we see after it.
  const v = state.video;
  if (j.frame <= 1 || !v.paceFrom || v.paceFrom.frame > j.frame) v.paceFrom = { frame: j.frame, elapsed: j.elapsed };
  const df = j.frame - v.paceFrom.frame;
  const rate = df > 0 ? df / (j.elapsed - v.paceFrom.elapsed) : 0;
  const elapsed = fmtTime(j.elapsed);
  switch (j.state) {
    case "running": {
      if (j.frame === 0) $("renderEmpty").textContent = "Calculando el primer keyframe (búsqueda completa, es el más lento)…";
      const eta = rate ? ` · faltan ~${fmtTime((j.total - j.frame) / rate)}` : "";
      $("computeStatus").textContent =
        `Keyframe ${j.frame} / ${j.total} · ${rate ? rate.toFixed(2) + " /s · " : ""}${elapsed}${eta}`;
      break;
    }
    case "done":
      $("computeStatus").textContent = `Listo: ${j.frame} keyframes en ${elapsed}.`;
      break;
    case "canceled":
      $("computeStatus").textContent = `Cancelado en el keyframe ${j.frame} / ${j.total}.`;
      break;
    case "error":
      $("computeStatus").textContent = "Error: " + j.error;
      break;
  }
  player.changed();
}

function resetRenderView() {
  $("renderEmpty").hidden = false;
  $("renderEmpty").textContent = "Todavía no hay animación. Ajustá los parámetros y tocá “Calcular animación”.";
  $("sourceVideo").src = `/api/videos/${state.video.id}/source`;
  player.reset();
  showJob(null);
}

// ---------- player ----------

// What the player shows: the video's animation, or the recording of the
// image being built. A source is { width, height, limit, audio, render(ctx, t, w, h) }.
function playerSource() {
  return state.video ? videoSource() : recording.source();
}

function videoSource() {
  const a = state.video?.anim;
  if (!a) return null;
  // While computing, only the contiguous prefix of keyframes can be played.
  const kfs = a.keyframes;
  let n = 0;
  while (kfs[n]) n++;
  if (!n) return null;
  const part = n === kfs.length ? a : { ...a, keyframes: kfs.slice(0, n) };
  return {
    width: a.width, height: a.height, audio: true,
    limit: state.video.job?.state === "done" ? a.duration : Math.max(0, (n - 1) / a.kfps),
    render: (ctx, t, w, h) => Motion.render(ctx, part, t, $("interp").value, w, h),
  };
}

// Plays the current source on a canvas. For videos the original (hidden)
// provides the audio and is the clock when it can play; otherwise a timer is.
const player = (() => {
  const canvas = $("player");
  const ctx = canvas.getContext("2d", { alpha: false });
  const video = $("sourceVideo");
  let t = 0, playing = false, last = 0, raf = 0, useVideo = false;

  function draw() {
    if ($("renderView").hidden) return;
    const src = playerSource();
    $("soundLabel").hidden = !src?.audio;
    if (!src) {
      $("renderEmpty").hidden = false;
      return;
    }
    $("renderEmpty").hidden = true;
    const box = $("playerBox");
    const scale = Math.min(box.clientWidth / src.width, box.clientHeight / src.height);
    const cssW = Math.max(1, Math.floor(src.width * scale)), cssH = Math.max(1, Math.floor(src.height * scale));
    const dpr = window.devicePixelRatio || 1;
    const w = Math.round(cssW * dpr), h = Math.round(cssH * dpr);
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
      canvas.style.width = cssW + "px";
      canvas.style.height = cssH + "px";
    }
    t = Math.min(t, src.limit);
    src.render(ctx, t, w, h);
    $("seek").max = Math.max(src.limit, 0.001);
    $("seek").value = t;
    $("clock").textContent = `${fmtClock(t)} / ${fmtClock(src.limit)}`;
  }

  function tick(now) {
    raf = 0;
    if (!playing) return;
    const src = playerSource();
    if (!src) return pause();
    if (useVideo && !video.paused && video.readyState >= 2) t = video.currentTime;
    else t += (now - last) / 1000;
    last = now;
    if (t >= src.limit) {
      t = 0;
      if (useVideo) video.currentTime = 0;
    }
    draw();
    raf = requestAnimationFrame(tick);
  }

  async function play() {
    const src = playerSource();
    if (!src) return;
    playing = true;
    $("play").textContent = "❚❚";
    last = performance.now();
    useVideo = false;
    if (src.audio) {
      try {
        video.muted = !$("sound").checked;
        video.currentTime = t;
        await video.play();
        useVideo = true;
      } catch { /* unsupported source: keep the timer clock, without audio */ }
    }
    if (!raf) raf = requestAnimationFrame(tick);
  }

  function pause() {
    playing = false;
    $("play").textContent = "▶";
    video.pause();
  }

  $("play").addEventListener("click", () => (playing ? pause() : play()));
  $("seek").addEventListener("input", () => {
    t = +$("seek").value;
    if (useVideo) video.currentTime = t;
    draw();
  });
  $("sound").addEventListener("change", () => { video.muted = !$("sound").checked; });
  $("interp").addEventListener("input", draw);
  new ResizeObserver(draw).observe($("playerBox"));

  return {
    toggle: () => (playing ? pause() : play()),
    play,
    pause,
    reset() { pause(); t = 0; },
    // Something changed: redraw (the timer loop does it when playing).
    changed() { if (!playing) draw(); },
    draw,
  };
})();

// ---------- image: recording the build ----------

const recording = (() => {
  const fill = (id, entries) =>
    $(id).replaceChildren(...entries.map(([v, label]) => new Option(label, v)));
  fill("recDirection", Object.entries(Build.DIRECTIONS));
  fill("recOrder", Object.entries(Build.ORDERS));
  fill("recCurve", Object.entries(Build.CURVES).map(([k, c]) => [k, c.label]));
  fill("recEffect", Object.entries(Build.EFFECTS));
  fill("sceneDepthMode", Object.entries(Scene3D.DEPTHS));
  fill("sceneAssembly", Object.entries(Scene3D.ASSEMBLIES));
  fill("sceneCamera", Object.entries(Scene3D.CAMERAS));
  const is3D = () => document.querySelector("#recStyle .on")?.dataset.value === "3d";
  $("recCurve").value = "outCubic";
  $("recEffect").value = "fade";

  const HINTS = {
    linear: "La misma cantidad de formas por segundo todo el tiempo.",
    in: "Empieza lento y va cada vez más rápido.",
    out: "Empieza rápido y va frenando.",
    inOut: "Arranca lento, acelera en el medio y frena al final.",
    outIn: "Arranca rápido, se calma en el medio y vuelve a acelerar.",
    impact: "Cada forma dura según cuánto mejora la imagen: las grandes del principio se ven con calma y los detalles pasan rápido.",
  };
  const hintFor = (c) => HINTS[c] ?? HINTS[c.startsWith("inOut") ? "inOut" : c.startsWith("outIn") ? "outIn" : c.startsWith("in") ? "in" : "out"];

  function opts() {
    const num = (id, lo, hi) => Math.min(hi, Math.max(lo, +$(id).value || 0));
    return {
      direction: $("recDirection").value,
      order: $("recOrder").value,
      curve: $("recCurve").value,
      effect: $("recEffect").value,
      effectTime: +$("recEffectTime").value,
      duration: num("recDuration", 0.5, 120),
      holdStart: num("recHoldStart", 0, 10),
      holdEnd: num("recHoldEnd", 0, 10),
      ...(is3D() && {
        style: "3d",
        depthMode: $("sceneDepthMode").value,
        depth: +$("sceneDepth").value,
        assembly: $("sceneAssembly").value,
        scatter: +$("sceneScatter").value,
        stagger: +$("sceneStagger").value,
        camera: $("sceneCamera").value,
        angle: +$("sceneAngle").value,
      }),
    };
  }

  // The shapes shown in the editor, as long as they have all arrived.
  function image() {
    if (state.video || !state.id || !state.image) return null;
    const want = Math.min(state.params?.shapes ?? 0, state.data.length);
    let n = 0;
    while (n < want && state.data[n]) n++;
    if (!n) return null;
    return { ...state.image, shapes: state.data.slice(0, n), scores: state.scores.slice(0, n), baseScore: state.baseScore };
  }

  let cache = { key: null, src: null };
  function source() {
    const img = image();
    if (!img) return null;
    const o = opts();
    const key = JSON.stringify([state.id, state.gen, img.shapes.length, o]);
    if (cache.key !== key) {
      const plan = o.style === "3d" ? Scene3D.plan(img, o) : Build.plan(img, o);
      cache = { key, src: { ...plan, audio: false, limit: plan.duration } };
    }
    return cache.src;
  }

  function drawCurve() {
    $("curveHint").textContent = hintFor($("recCurve").value);
  }

  function updateRecOutputs() {
    $("recEffectTimeOut").textContent = (+$("recEffectTime").value).toFixed(2) + " s";
    $("recEffectTime").disabled = $("recEffect").value === "none";
    $("sceneDepthOut").textContent = (+$("sceneDepth").value).toFixed(2) + "×";
    $("sceneScatterOut").textContent = (+$("sceneScatter").value).toFixed(2) + "×";
    $("sceneStaggerOut").textContent = (+$("sceneStagger").value).toFixed(2);
    $("sceneAngleOut").textContent = $("sceneAngle").value + "°";
    $("sceneAngle").disabled = $("sceneCamera").value === "static";
    $("rec3d").hidden = !is3D();
    $("rec2d").hidden = is3D();
  }
  const recInputs = ["recDirection", "recOrder", "recCurve", "recEffect", "recEffectTime", "recDuration", "recHoldStart", "recHoldEnd",
    "sceneDepthMode", "sceneDepth", "sceneAssembly", "sceneScatter", "sceneStagger", "sceneCamera", "sceneAngle"];
  for (const id of recInputs) {
    $(id).addEventListener("input", () => {
      updateRecOutputs();
      drawCurve();
      player.changed();
      Picker.refresh();
    });
  }
  $("recStyle").addEventListener("click", (e) => {
    const b = e.target.closest("button");
    if (!b) return;
    for (const x of $("recStyle").children) x.classList.toggle("on", x === b);
    updateRecOutputs();
    player.changed();
    Picker.refresh();
  });
  updateRecOutputs();

  $("recPreview").addEventListener("click", () => {
    showTab("render");
    player.reset();
    player.play();
  });

  let abort = null;
  $("recCancel").addEventListener("click", () => abort?.abort());
  $("recExport").addEventListener("click", async () => {
    const src = source();
    if (!src) return;
    const status = $("recStatus");
    abort = new AbortController();
    $("recExport").disabled = true;
    $("recCancel").hidden = false;
    $("recProgress").hidden = false;
    const started = performance.now();
    try {
      const res = await Motion.encodeMP4(src, {
        fps: +$("recFps").value,
        longSide: +$("recSize").value,
        signal: abort.signal,
        onProgress: (f) => {
          $("recProgress").value = f;
          status.textContent = `Codificando… ${Math.round(f * 100)}%`;
        },
      });
      download(res.blob, "primitive-construccion.mp4");
      status.textContent = `MP4 listo: ${res.width}×${res.height}, ${res.codec === "avc" ? "H.264" : "VP9"}, ` +
        `${src.duration.toFixed(1)} s, ${(res.blob.size / 1e6).toFixed(1)} MB, en ${((performance.now() - started) / 1000).toFixed(1)} s.`;
    } catch (e) {
      status.textContent = e.name === "AbortError" ? "Exportación cancelada." : "Error: " + e.message;
    } finally {
      abort = null;
      $("recExport").disabled = false;
      $("recCancel").hidden = true;
      $("recProgress").hidden = true;
    }
  });

  // ---- visual pickers: each option previews itself with the current image ----

  // Stand-in image for the previews until the editor has shapes.
  function hsl(h, s, l) {
    const a = s * Math.min(l, 1 - l);
    const f = (n) => { const k = (n + h / 30) % 12; return 255 * (l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1))); };
    return [f(0), f(8), f(4)];
  }
  const DEMO = (() => {
    let seed = 7;
    const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
    const W = 120, Hh = 90, shapes = [], scores = [];
    for (let i = 0; i < 70; i++) {
      const size = 50 * (1 - i / 80) + 6;
      const x = rnd() * W, y = rnd() * Hh;
      const [r, g, bl] = hsl((x / W) * 240 + (y / Hh) * 60, 0.6, 0.55);
      shapes.push([0, r, g, bl, 170, 1,
        x + (rnd() - 0.5) * size, y - size / 2, x - size / 2, y + size / 2, x + size / 2, y + (rnd() - 0.3) * size]);
      scores.push(1 - Math.sqrt((i + 1) / 70) * 0.8);
    }
    return { width: W, height: Hh, background: "#2a3242", shapes, scores, baseScore: 1 };
  })();
  const thumbImage = () => image() ?? DEMO;
  const planFor = (o) => (o.style === "3d" ? Scene3D.plan(thumbImage(), o) : Build.plan(thumbImage(), o));
  // Short loops so every card shows its whole story in a couple of seconds.
  const quick = () => ({ ...opts(), duration: 2.2, holdStart: 0.2, holdEnd: 0.8, effectTime: Math.min(+$("recEffectTime").value, 0.6) });
  const variant = (key) => (value) => planFor({ ...quick(), [key]: value });

  Picker.attach($("recDirection"), variant("direction"));
  Picker.attach($("recOrder"), variant("order"));
  Picker.attach($("recEffect"), (value) => Build.plan(thumbImage(), { ...quick(), style: "2d", effect: value, curve: "linear", order: "original" }));
  // Curves: the plot of the curve, a dot running along it, and a bar showing
  // how much of the image is built.
  Picker.attach($("recCurve"), (value) => {
    const ease = value === "impact" ? Build.plan(thumbImage(), { ...quick(), curve: "impact" }).ease : Build.CURVES[value].f;
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
  });
  // 3D: depth previews sway the camera over the finished image to show the
  // layering; assembly and camera previews play the whole build.
  Picker.attach($("sceneDepthMode"), (value) => Scene3D.plan(thumbImage(),
    { ...quick(), style: "3d", depthMode: value, assembly: "none", camera: "sway", angle: Math.max(30, +$("sceneAngle").value), duration: 0.05, holdStart: 0, holdEnd: 3 }));
  Picker.attach($("sceneAssembly"), variant("assembly"));
  // Camera previews keep the shapes in place (just fading in) so what you
  // see is the camera's movement, not pieces flying out of frame.
  Picker.attach($("sceneCamera"), (value) => Scene3D.plan(thumbImage(), {
    ...quick(), style: "3d", direction: "build", camera: value, assembly: "none", stagger: 0,
    angle: Math.max(35, +$("sceneAngle").value), duration: 1.8, holdStart: 0, holdEnd: 1.6,
  }));

  drawCurve();
  return { source, drawCurve };
})();

// ---------- video: exporting the animation ----------

function download(blob, name) {
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 60_000);
}

let exportAbort = null;
$("cancelExport").addEventListener("click", () => exportAbort?.abort());

for (const b of document.querySelectorAll("[data-anim-export]")) {
  b.addEventListener("click", async () => {
    const v = state.video;
    const anim = v?.anim;
    if (!anim || v.job?.state !== "done") return;
    const opts = { mode: $("interp").value, fps: +$("outFps").value, longSide: +$("outSize").value };
    const status = $("exportStatus");
    const buttons = document.querySelectorAll("[data-anim-export]");
    buttons.forEach((x) => { x.disabled = true; });
    try {
      switch (b.dataset.animExport) {
        case "json":
          download(new Blob([JSON.stringify(anim)], { type: "application/json" }), "primitive-keyframes.json");
          status.textContent = "Keyframes descargados.";
          break;
        case "lottie": {
          const json = JSON.stringify(Motion.lottie(anim, opts));
          download(new Blob([json], { type: "application/json" }), "primitive-lottie.json");
          status.textContent = `Lottie descargado (${(json.length / 1e6).toFixed(1)} MB).`;
          break;
        }
        case "mp4": {
          exportAbort = new AbortController();
          $("cancelExport").hidden = false;
          $("exportProgress").hidden = false;
          const started = performance.now();
          const res = await Motion.exportMP4(anim, {
            ...opts,
            signal: exportAbort.signal,
            onProgress: (f) => {
              $("exportProgress").value = f;
              status.textContent = `Codificando… ${Math.round(f * 100)}%`;
            },
          });
          let blob = res.blob;
          if ($("audio").checked && v.info.hasAudio) {
            status.textContent = "Agregando el audio…";
            const mixed = await fetch(`/api/videos/${v.id}/mux`, { method: "POST", body: blob });
            if (!mixed.ok) throw new Error("al agregar el audio: " + (await mixed.text()));
            blob = await mixed.blob();
          }
          download(blob, "primitive.mp4");
          const secs = ((performance.now() - started) / 1000).toFixed(1);
          status.textContent = `MP4 listo: ${res.width}×${res.height}, ${res.codec === "avc" ? "H.264" : "VP9"}, ` +
            `${(blob.size / 1e6).toFixed(1)} MB, en ${secs} s.`;
          break;
        }
      }
    } catch (e) {
      status.textContent = e.name === "AbortError" ? "Exportación cancelada." : "Error: " + e.message;
    } finally {
      exportAbort = null;
      $("cancelExport").hidden = true;
      $("exportProgress").hidden = true;
      buttons.forEach((x) => { x.disabled = false; });
    }
  });
}

// ---------- tabs, compare & export ----------

function showTab(name) {
  const isRender = name === "render";
  for (const b of document.querySelectorAll("#tabs button")) b.classList.toggle("on", b.dataset.tab === name);
  $("view").hidden = isRender;
  $("renderView").hidden = !isRender;
  $("compare").hidden = isRender || !state.id;
  if (isRender) player.draw();
  else player.pause();
}
$("tabs").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (b) showTab(b.dataset.tab);
});

function showOriginal(on) {
  $("original").hidden = !on;
  $("out").style.visibility = on ? "hidden" : "";
}
const cmp = $("compare");
cmp.addEventListener("pointerdown", () => showOriginal(true));
for (const ev of ["pointerup", "pointerleave", "pointercancel"]) {
  cmp.addEventListener(ev, () => showOriginal(false));
}
document.addEventListener("keydown", (e) => {
  if (e.code !== "Space" || e.target !== document.body || e.repeat) return;
  e.preventDefault();
  if (!$("renderView").hidden) player.toggle();
  else if (state.id) showOriginal(true);
});
document.addEventListener("keyup", (e) => { if (e.code === "Space") showOriginal(false); });

for (const b of document.querySelectorAll("[data-export]")) {
  b.addEventListener("click", () => {
    const n = Math.min(state.params.shapes, state.shapes.length);
    const size = $("exportSize").value || 1024;
    location.href = `/api/sessions/${state.id}/export?format=${b.dataset.export}&size=${size}&n=${n}`;
  });
}

updateOutputs();
