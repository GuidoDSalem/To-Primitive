"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  id: null,
  events: null,
  gen: 0,
  shapes: [],   // SVG fragments of the current run, in order
  score: 1,
  running: false,
  params: null,
  rate: { count: 0, time: 0, value: 0 },
};

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
}

let sendTimer = null;
function sendParams(extra = {}, delay = 120) {
  if (!state.id) return;
  state.params = readParams();
  Object.assign(state.params, extra);
  updateOutputs();
  scheduleRender();
  clearTimeout(sendTimer);
  sendTimer = setTimeout(() => {
    fetch(`/api/sessions/${state.id}/params`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...state.params, restart: !!extra.restart }),
    }).catch((e) => console.error(e));
  }, delay);
}

for (const id of ["shapes", "alpha", "alphaAuto", "repeat", "inputSize", "background", "bgAuto"]) {
  $(id).addEventListener("input", () => sendParams());
}
$("mode").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  setMode(+b.dataset.value);
  sendParams({}, 0);
});
$("pause").addEventListener("click", () => sendParams({ paused: !state.params.paused }, 0));
$("restart").addEventListener("click", () => sendParams({ restart: true }, 0));

// ---------- rendering ----------

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
  let status = state.running ? "Generando…" : p.paused ? "Pausado" : "Listo";
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

// ---------- server events ----------

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
    const svg = $("out");
    svg.setAttribute("viewBox", `0 0 ${d.width} ${d.height}`);
    for (const r of [$("bg"), $("clipRect")]) {
      r.setAttribute("width", d.width);
      r.setAttribute("height", d.height);
    }
    $("bg").setAttribute("fill", d.background);
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

// ---------- upload ----------

async function upload(file) {
  if (!file || !file.type.startsWith("image/")) {
    alert("Por ahora solo se aceptan imágenes.");
    return;
  }
  $("stats").textContent = "Subiendo…";
  const form = new FormData();
  form.append("image", file);
  const res = await fetch("/api/sessions", { method: "POST", body: form });
  if (!res.ok) {
    $("stats").textContent = "Error: " + (await res.text());
    return;
  }
  const d = await res.json();

  const keep = state.params; // carry the current settings over to the new image
  state.id = d.id;
  state.params = null;
  state.shapes = [];
  const img = $("original");
  if (img.src) URL.revokeObjectURL(img.src);
  img.src = URL.createObjectURL(file);

  $("drop").hidden = true;
  $("view").hidden = false;
  $("compare").hidden = false;
  $("controls").disabled = false;

  connect();
  if (keep) {
    writeParams(keep);
    sendParams({ paused: false }, 0);
  }
}

$("file").addEventListener("change", (e) => upload(e.target.files[0]));

const stage = $("stage");
stage.addEventListener("dragover", (e) => { e.preventDefault(); stage.classList.add("dragging"); });
stage.addEventListener("dragleave", () => stage.classList.remove("dragging"));
stage.addEventListener("drop", (e) => {
  e.preventDefault();
  stage.classList.remove("dragging");
  upload(e.dataTransfer.files[0]);
});
$("drop").addEventListener("click", () => $("file").click());

// ---------- compare & export ----------

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
