"use strict";

// Generator page: settings (on/off, which templates, languages), uploading
// photos and videos to the input folder, and how far each source got.

const $ = (id) => document.getElementById(id);
let view = null;
let dirty = false; // settings form has unsaved changes

async function api(path, opts = {}) {
  if (opts.json !== undefined) {
    opts = { ...opts, headers: { "Content-Type": "application/json" }, body: JSON.stringify(opts.json) };
  }
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  return res.status === 204 ? null : res.json();
}

const enc = encodeURIComponent;
const name = (t) => Templates.LIST[t]?.name ?? t;
const kinds = (t) => (Templates.LIST[t]?.kinds ?? []).map((k) => (k === "image" ? "foto" : "video")).join(" y ");
const fmtWhen = (iso) => new Date(iso).toLocaleString("es-AR", { weekday: "short", day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" });
const fmtDur = (s) => `${Math.floor(s / 60)}:${String(Math.round(s % 60)).padStart(2, "0")}`;

function showSettings(o) {
  $("enabled").checked = o.settings.enabled;
  $("ask").checked = !o.settings.skipChoice;
  const on = new Set(o.settings.templates.length ? o.settings.templates : o.templates);
  $("templates").replaceChildren(...o.templates.map((t) => {
    const label = document.createElement("label");
    label.className = "check";
    const box = Object.assign(document.createElement("input"), { type: "checkbox", checked: on.has(t) });
    box.dataset.value = t;
    box.addEventListener("input", () => { dirty = true; });
    label.append(box, ` ${name(t)} `, Object.assign(document.createElement("span"), { className: "kinds", textContent: `(${kinds(t)})` }));
    return label;
  }));
  $("langs").value = o.settings.languages.join(", ");
  $("llm").value = o.settings.llm.provider || "";
  $("llmModel").value = o.settings.llm.model || "";
  $("llmBase").value = o.settings.llm.baseURL || "";
  $("llmLocal").hidden = $("llm").value !== "local";
  $("llmModel").placeholder = $("llm").value === "local" ? "llama3.1" : $("llm").value === "none" ? "—" : "claude-opus-5-5";
  dirty = false;
}
for (const id of ["enabled", "ask", "langs", "llm", "llmModel", "llmBase"]) {
  $(id).addEventListener("input", () => {
    dirty = true;
    $("llmLocal").hidden = $("llm").value !== "local";
  });
}

$("save").addEventListener("click", async () => {
  const boxes = [...$("templates").querySelectorAll("input")];
  const chosen = boxes.filter((b) => b.checked).map((b) => b.dataset.value);
  try {
    show(await api("/api/auto/settings", { method: "PUT", json: {
      ...view?.settings, // what other pages set (e.g. skipRefine)
      enabled: $("enabled").checked, skipChoice: !$("ask").checked, templates: chosen.length === boxes.length ? [] : chosen,
      languages: $("langs").value.split(",").map((l) => l.trim().toLowerCase()).filter(Boolean),
      llm: { provider: $("llm").value, model: $("llmModel").value.trim(), baseURL: $("llmBase").value.trim() },
    } }), true);
    $("saveStatus").textContent = "Guardado.";
  } catch (e) {
    $("saveStatus").textContent = "Error: " + e.message;
  }
});

async function act(promise) {
  try {
    const o = await promise;
    if (o) show(o);
    else refresh();
  } catch (e) {
    alert(e.message);
    refresh();
  }
}

function button(text, title, onClick) {
  const b = Object.assign(document.createElement("button"), { type: "button", textContent: text, title });
  b.addEventListener("click", onClick);
  return b;
}

// ---------- uploads ----------

const uploads = []; // { name, fraction, error } while uploading

function upload(file) {
  const u = { name: file.name, fraction: 0, error: null };
  uploads.push(u);
  drawSources();
  return new Promise((resolve) => {
    const form = new FormData();
    form.append("file", file);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/auto/inputs");
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) u.fraction = e.loaded / e.total;
      drawSources();
    };
    xhr.onload = async () => {
      if (xhr.status === 200) uploads.splice(uploads.indexOf(u), 1);
      else u.error = xhr.responseText || xhr.statusText;
      await refresh();
      resolve();
    };
    xhr.onerror = () => {
      u.error = "no se pudo subir";
      drawSources();
      resolve();
    };
    xhr.send(form);
  });
}

async function uploadAll(files) {
  const list = [...files].filter((f) => /^(image|video)\//.test(f.type) || /\.(jpe?g|png|webp|gif|mp4|mov|m4v|webm|mkv|avi)$/i.test(f.name));
  if (list.length < files.length) alert("Solo se suben fotos y videos.");
  for (const f of list) await upload(f); // one at a time: the first starts generating sooner
}

$("pick").addEventListener("click", () => $("files").click());
$("files").addEventListener("change", (e) => {
  uploadAll(e.target.files);
  e.target.value = "";
});
const stage = $("stage");
stage.addEventListener("dragover", (e) => {
  e.preventDefault();
  stage.classList.add("dragging");
});
stage.addEventListener("dragleave", (e) => {
  if (!stage.contains(e.relatedTarget)) stage.classList.remove("dragging");
});
stage.addEventListener("drop", (e) => {
  e.preventDefault();
  stage.classList.remove("dragging");
  uploadAll(e.dataTransfer.files);
});
$("enableNow").addEventListener("click", () => act(api("/api/auto/settings", { method: "PUT", json: { ...view.settings, enabled: true } })));

// ---------- choosing the templates of a new source ----------

const choices = new Map(); // source name → { tpls: Set, langs: string }, kept across refreshes
let waitingKey = null;

const langsOf = (text) => text.split(",").map((l) => l.trim().toLowerCase()).filter((l) => /^[a-z]{2}$/.test(l));

function waitingCard(w) {
  const c = choices.get(w.name) ?? { tpls: new Set(), langs: view.settings.languages.join(", ") || "es, en" };
  choices.set(w.name, c);
  const card = document.createElement("div");
  card.className = "wcard";
  const url = `/api/auto/inputs/${enc(w.name)}`;
  const thumb = w.kind === "video"
    ? Object.assign(document.createElement("video"), { src: url + "#t=1", muted: true, loop: true, playsInline: true, preload: "metadata" })
    : Object.assign(document.createElement("img"), { src: url, alt: "" });
  thumb.className = "thumb";
  if (w.kind === "video") {
    thumb.addEventListener("mouseenter", () => thumb.play().catch(() => {}));
    thumb.addEventListener("mouseleave", () => thumb.pause());
  }
  const body = document.createElement("div");
  body.className = "body";
  const head = document.createElement("div");
  head.className = "head";
  head.append(Object.assign(document.createElement("strong"), { textContent: w.name }), Object.assign(document.createElement("span"), { textContent: w.kind === "video" ? "video" : "foto" }));

  const grid = document.createElement("div");
  grid.className = "tpl-grid";
  const count = Object.assign(document.createElement("span"), { className: "n" });
  const go = button("Generar", "Hace un reel por template e idioma", async () => {
    go.disabled = true;
    try {
      show(await api(`${url}/start`, { method: "POST", json: { templates: [...c.tpls], languages: langsOf(c.langs) } }));
      choices.delete(w.name);
    } catch (e) {
      alert(e.message);
      go.disabled = false;
    }
  });
  go.className = "primary";
  const update = () => {
    const n = c.tpls.size * Math.max(1, langsOf(c.langs).length);
    count.textContent = c.tpls.size ? `${n} ${n === 1 ? "versión" : "versiones"}` : "Marcá al menos un template";
    go.disabled = !c.tpls.size;
    for (const el of grid.children) el.classList.toggle("on", c.tpls.has(el.dataset.value));
  };
  const tpls = view.byKind[w.kind] ?? [];
  grid.append(...tpls.map((t) => {
    const label = Object.assign(document.createElement("label"), { className: "tpl" });
    label.dataset.value = t;
    const box = Object.assign(document.createElement("input"), { type: "checkbox", checked: c.tpls.has(t) });
    box.addEventListener("input", () => {
      box.checked ? c.tpls.add(t) : c.tpls.delete(t);
      update();
    });
    const text = document.createElement("span");
    text.append(name(t), Object.assign(document.createElement("small"), { textContent: Templates.LIST[t]?.desc ?? "" }));
    label.append(box, text);
    return label;
  }));
  const all = button("Todos", "", () => {
    const every = tpls.every((t) => c.tpls.has(t));
    c.tpls = new Set(every ? [] : tpls);
    for (const box of grid.querySelectorAll("input")) box.checked = c.tpls.has(box.closest(".tpl").dataset.value);
    all.textContent = every ? "Todos" : "Ninguno";
    update();
  });
  const langs = Object.assign(document.createElement("input"), { type: "text", value: c.langs, title: "Idiomas, separados por comas" });
  langs.addEventListener("input", () => {
    c.langs = langs.value;
    update();
  });
  const drop = button("Descartar", "No genera nada; el archivo pasa a descartados/ en la entrada", () => {
    if (confirm(`¿Descartar ${w.name}?`)) act(api(`${url}/dismiss`, { method: "POST" }));
  });
  const foot = document.createElement("div");
  foot.className = "foot";
  foot.append(go, count, all, "Idiomas", langs, drop);
  body.append(head, grid, foot);
  card.append(thumb, body);
  update();
  return card;
}

function drawWaiting() {
  const ws = view?.waiting ?? [];
  $("waitingBox").hidden = !ws.length;
  $("waitCount").textContent = ws.length > 1 ? ws.length : "";
  // Rebuilt only when the list changes, so a refresh doesn't get in the way.
  const key = ws.map((w) => w.name).join("|");
  if (key === waitingKey) return;
  waitingKey = key;
  $("waiting").replaceChildren(...ws.map(waitingCard));
}

// ---------- sources in progress ----------

// One line per source: how many of its versions are ready, the one being
// made, and the failed ones (to retry).
function sourceRow(src, jobs) {
  const row = document.createElement("div");
  const done = jobs.filter((j) => j.state === "done").length;
  const failed = jobs.filter((j) => j.state === "error");
  const running = jobs.find((j) => j.state === "running");
  const finished = done + failed.length === jobs.length;
  row.className = "job " + (running ? "running" : failed.length ? "error" : finished ? "done" : "queued");
  const what = Object.assign(document.createElement("div"), { className: "what" });
  what.append(Object.assign(document.createElement("strong"), { textContent: src }));
  if (running) what.append(" ", Object.assign(document.createElement("small"), { textContent: `· ${name(running.template)} · ${(running.lang || "es").toUpperCase()}${running.stage ? " · " + running.stage : ""}` }));
  const state = Object.assign(document.createElement("div"), { className: "state" });
  state.textContent = finished && !failed.length ? `${done} versiones listas` : `${done} de ${jobs.length} listas` + (failed.length ? ` · ${failed.length} fallaron` : "");
  row.append(what, state);
  if (!finished) {
    row.append(Object.assign(document.createElement("progress"), { max: 1, value: (done + (running?.progress || 0)) / jobs.length }));
  }
  if (failed.length) {
    row.append(Object.assign(document.createElement("div"), { className: "err", textContent: failed[0].error }));
    row.append(button("Reintentar", "Vuelven a la cola las que fallaron", () =>
      act(Promise.all(failed.map((j) => api(`/api/auto/jobs/${j.id}/retry`, { method: "POST" }))).then(() => null))));
  }
  return row;
}

function uploadRow(u) {
  const row = document.createElement("div");
  row.className = "job " + (u.error ? "error" : "running");
  row.append(Object.assign(document.createElement("div"), { className: "what", textContent: u.name }),
    Object.assign(document.createElement("div"), { className: "state", textContent: u.error ? "No se subió" : `Subiendo ${Math.round(u.fraction * 100)} %` }));
  if (u.error) {
    row.append(Object.assign(document.createElement("div"), { className: "err", textContent: u.error }));
    row.append(button("Quitar", "", () => {
      uploads.splice(uploads.indexOf(u), 1);
      drawSources();
    }));
  } else {
    row.append(Object.assign(document.createElement("progress"), { max: 1, value: u.fraction }));
  }
  return row;
}

function drawSources() {
  // Jobs come newest first; the copies to other languages live in Publicar.
  const by = new Map();
  for (const j of view?.jobs ?? []) {
    if (j.dest === "queue") continue;
    if (!by.has(j.source)) by.set(j.source, []);
    by.get(j.source).push(j);
  }
  const rows = [...by].slice(0, 12).map(([src, jobs]) => sourceRow(src, jobs));
  $("sources").replaceChildren(...uploads.map(uploadRow), ...rows);
  if (!uploads.length && !rows.length) $("sources").append(Object.assign(document.createElement("p"), { className: "none-yet", textContent: "Nada todavía: subí fotos o videos." }));
  const active = (view?.jobs ?? []).filter((j) => j.dest !== "queue" && (j.state === "queued" || j.state === "running")).length;
  $("jobCount").textContent = active ? `${active} versiones pendientes` : "";
}

function show(o, settingsToo = false) {
  view = o;
  if (settingsToo || !dirty) showSettings(o);
  $("inInfo").textContent = `Entrada: ${o.in} (las fuentes ya hechas pasan a procesados/)`;
  $("outInfo").textContent = `Generados: ${o.out}`;
  $("writerInfo").textContent = `Escribe: ${o.writer}.` + (o.settings.llm.provider === "anthropic" && !o.hasClaudeKey ? " ⚠ Falta ANTHROPIC_API_KEY en el .env: se van a usar las reglas." : "");
  $("browserInfo").textContent = o.browser ? "Navegador: " + o.browser.split("/").pop() : "⚠ No hay navegador para generar: instalá Chromium o definí CHROME_BIN.";
  $("paused").hidden = o.settings.enabled;
  drawWaiting();
  drawSources();
  $("outCount").textContent = o.outputs.length;
  $("outSummary").textContent = o.outputs.length
    ? `${o.outputs.length} versiones esperan revisión: elegí una por idioma, refinala y mandala a la cola.`
    : "Todavía no se generó nada.";
  $("log").replaceChildren(...o.log.slice(0, 25).map((e) => {
    const li = document.createElement("li");
    if (e.action === "error") li.className = "error";
    li.append(`${fmtWhen(e.time)} · `, Object.assign(document.createElement("strong"), { textContent: e.action }), e.name ? ` ${e.name}` : "", e.detail ? ` — ${e.detail}` : "");
    return li;
  }));
}

async function refresh() {
  try {
    show(await api("/api/auto"));
  } catch (e) {
    $("sources").textContent = "No se pudo leer el generador: " + e.message;
  }
}
$("refresh").addEventListener("click", refresh);
refresh();
// Faster while something is being generated.
(function poll() {
  const busy = view?.jobs.some((j) => j.state === "running");
  setTimeout(() => { if (!document.hidden) refresh(); poll(); }, busy ? 3000 : 15000);
})();
