"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  id: null,     // active editing session (an image, or a frame of the video)
  events: null,
  gen: 0,
  shapes: [],   // SVG fragments of the current run, in order
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

function sizeSVG(prefix, d) {
  $(prefix === "r" ? "renderOut" : "out").setAttribute("viewBox", `0 0 ${d.width} ${d.height}`);
  for (const r of [$(prefix + "bg"), $(prefix + "clipRect")]) {
    r.setAttribute("width", d.width);
    r.setAttribute("height", d.height);
  }
  $(prefix + "bg").setAttribute("fill", d.background);
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
    state.score = d.score;
    state.running = d.running;
    state.rate = { count: d.shapes.length, time: 0, value: 0 };
    // Keep local control values if the user is mid-drag; adopt the server's otherwise.
    if (!state.params) {
      state.params = d.params;
      writeParams(d.params);
    }
    sizeSVG("", d);
    if ($("bgAuto").checked) $("background").value = d.background;
    $("shapesG").replaceChildren();
    scheduleRender();
  });

  es.addEventListener("shape", (e) => {
    const d = JSON.parse(e.data);
    if (d.g !== state.gen) return;
    state.shapes[d.i] = d.svg;
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
  showTab("editor");
  connect();
}

// ---------- upload ----------

async function upload(file) {
  if (!file) return;
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
  state.video = { id: d.id, info, frames: [], active: -1, job: null };
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
  fillFPS(info.fps);
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
  $("tabs").hidden = true;
  $("frames").replaceChildren();
  $("exportTitle").textContent = "Exportar";
}

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

// ---------- video: render ----------

function fillFPS(fps) {
  const opts = [[0, `Original (${+fps.toFixed(2)})`]];
  for (const f of [30, 24, 15, 12, 8]) if (f < fps - 0.5) opts.push([f, String(f)]);
  $("fps").replaceChildren(...opts.map(([v, label]) => new Option(label, v)));
}

$("render").addEventListener("click", async () => {
  const v = state.video;
  // Free the CPU for the render.
  sendParams({ paused: true }, 0);
  const opts = {
    ...readParams(),
    paused: false,
    fps: +$("fps").value,
    adapt: +$("adapt").value,
    size: +$("videoSize").value,
    audio: $("audio").checked,
  };
  try {
    await api(`/api/videos/${v.id}/render`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(opts),
    });
    showTab("render");
  } catch (e) {
    alert("No se pudo iniciar el render: " + e.message);
  }
});
$("cancelRender").addEventListener("click", () =>
  api(`/api/videos/${state.video.id}/render`, { method: "DELETE" }));

function connectVideo() {
  const v = state.video;
  const es = new EventSource(`/api/videos/${v.id}/events`);
  v.events = es;
  es.addEventListener("job", (e) => {
    v.job = JSON.parse(e.data);
    showJob(v.job);
  });
  es.addEventListener("preview", (e) => {
    const d = JSON.parse(e.data);
    sizeSVG("r", d);
    $("rshapes").innerHTML = d.svg;
    $("renderOut").hidden = false;
    $("result").hidden = true;
    $("renderEmpty").hidden = true;
  });
}

function showJob(j) {
  const running = j?.state === "running";
  $("render").hidden = running;
  $("cancelRender").hidden = !running;
  $("progress").hidden = !j;
  $("download").hidden = j?.state !== "done";
  if (!j) {
    $("renderStatus").textContent = "";
    return;
  }
  $("progress").value = j.total ? j.frame / j.total : 0;
  // The first frame is a full search and much slower than the rest, so
  // measure the pace from the first frame we see after it.
  const v = state.video;
  if (j.frame <= 1 || !v.paceFrom || v.paceFrom.frame > j.frame) v.paceFrom = { frame: j.frame, elapsed: j.elapsed };
  const df = j.frame - v.paceFrom.frame;
  const fps = df > 0 ? df / (j.elapsed - v.paceFrom.elapsed) : 0;
  const elapsed = fmtTime(j.elapsed);
  switch (j.state) {
    case "running": {
      if (j.frame === 0) $("renderEmpty").textContent = "Generando el primer frame (búsqueda completa, es el más lento)…";
      const eta = fps ? ` · faltan ~${fmtTime((j.total - j.frame) / fps)}` : "";
      $("renderStatus").textContent =
        `Frame ${j.frame} / ${j.total} · ${fps ? fps.toFixed(2) + " frames/s · " : ""}${elapsed}${eta}`;
      break;
    }
    case "done": {
      $("renderStatus").textContent = `Listo: ${j.frame} frames en ${elapsed}.`;
      const url = `/api/videos/${state.video.id}/output?v=${Date.now()}`;
      $("download").href = url + "&download=1";
      $("result").src = url;
      $("result").hidden = false;
      $("renderOut").hidden = true;
      $("renderEmpty").hidden = true;
      break;
    }
    case "canceled":
      $("renderStatus").textContent = `Cancelado en el frame ${j.frame} / ${j.total}.`;
      break;
    case "error":
      $("renderStatus").textContent = "Error: " + j.error;
      break;
  }
}

function resetRenderView() {
  $("result").removeAttribute("src");
  $("result").hidden = true;
  $("renderOut").hidden = true;
  $("renderEmpty").hidden = false;
  $("renderEmpty").textContent = "Todavía no hay render. Ajustá los parámetros y tocá “Renderizar video”.";
  showJob(null);
}

// ---------- tabs, compare & export ----------

function showTab(name) {
  const isRender = name === "render";
  for (const b of document.querySelectorAll("#tabs button")) b.classList.toggle("on", b.dataset.tab === name);
  $("view").hidden = isRender;
  $("renderView").hidden = !isRender;
  $("compare").hidden = isRender || !state.id;
  if (!isRender) $("result").pause();
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
  if (e.code === "Space" && state.id && e.target === document.body) { e.preventDefault(); showOriginal(true); }
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
