"use strict";

// Generator page: settings (on/off, which templates), the jobs (one per
// source and template) and the reels generated, which can go to the
// publishing queue.

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
for (const id of ["enabled", "langs", "llm", "llmModel", "llmBase"]) {
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
      enabled: $("enabled").checked, templates: chosen.length === boxes.length ? [] : chosen,
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

const STATES = { queued: "En espera", running: "Generando", done: "Listo", error: "Falló" };

function jobRow(j) {
  const row = document.createElement("div");
  row.className = "job " + j.state;
  const what = Object.assign(document.createElement("div"), { className: "what" });
  what.append(Object.assign(document.createElement("strong"), { textContent: j.source }), ` · ${name(j.template)} · ${(j.lang || "es").toUpperCase()}`);
  const state = Object.assign(document.createElement("div"), { className: "state" });
  state.textContent = STATES[j.state] + (j.state === "running" && j.stage ? ` · ${j.stage}` : "") + (j.state === "done" && j.output ? ` → ${j.output}` : "");
  row.append(what, state);
  if (j.state === "running") {
    row.append(Object.assign(document.createElement("progress"), { max: 1, value: j.progress || 0 }));
  }
  if (j.state === "error") {
    row.append(Object.assign(document.createElement("div"), { className: "err", textContent: j.error }));
    row.append(button("Reintentar", "Vuelve a la cola", () => act(api(`/api/auto/jobs/${j.id}/retry`, { method: "POST" }))));
  }
  return row;
}

function show(o, settingsToo = false) {
  view = o;
  if (settingsToo || !dirty) showSettings(o);
  $("inInfo").textContent = `Entrada: ${o.in} (las fuentes ya hechas pasan a procesados/)`;
  $("outInfo").textContent = `Generados: ${o.out}`;
  $("writerInfo").textContent = `Escribe: ${o.writer}.` + (o.settings.llm.provider === "anthropic" && !o.hasClaudeKey ? " ⚠ Falta ANTHROPIC_API_KEY en el .env: se van a usar las reglas." : "");
  $("browserInfo").textContent = o.browser ? "Navegador: " + o.browser.split("/").pop() : "⚠ No hay navegador para generar: instalá Chromium o definí CHROME_BIN.";
  const active = o.jobs.filter((j) => j.state === "queued" || j.state === "running").length;
  $("jobCount").textContent = active ? `${active} pendientes` : "";
  $("jobs").replaceChildren(...(o.jobs.length ? o.jobs.slice(0, 60).map(jobRow) : [Object.assign(document.createElement("p"), { className: "none-yet", textContent: "Dejá fotos o videos en la carpeta de entrada." })]));
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
    $("jobs").textContent = "No se pudo leer el generador: " + e.message;
  }
}
$("refresh").addEventListener("click", refresh);
refresh();
// Faster while something is being generated.
(function poll() {
  const busy = view?.jobs.some((j) => j.state === "running");
  setTimeout(() => { if (!document.hidden) refresh(); poll(); }, busy ? 3000 : 15000);
})();
