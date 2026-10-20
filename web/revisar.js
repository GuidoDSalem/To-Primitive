"use strict";

// Review page: the versions generated from each source, to choose one per
// language, refine it in the editor and send it to the publishing queue.

const $ = (id) => document.getElementById(id);
let view = null;

async function api(path, opts = {}) {
  if (opts.json !== undefined) {
    opts = { ...opts, headers: { "Content-Type": "application/json" }, body: JSON.stringify(opts.json) };
  }
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  return res.status === 204 ? null : res.json();
}

const enc = encodeURIComponent;
const tplName = (t) => Templates.LIST[t]?.name ?? t ?? "Reel";
const fmtDur = (s) => `${Math.floor(s / 60)}:${String(Math.round(s % 60)).padStart(2, "0")}`;
const STAGES = { generated: "Generada", chosen: "Elegida", refined: "Refinada" };
const videoURL = (v, discarded) => discarded ? `/api/review/discarded/${enc(v.name)}/video` : `/api/auto/outputs/${enc(v.name)}/video`;
const refineURL = (v) => `reels.html?refine=${enc(v.name)}`;
const canQueue = (v) => v.stage === "refined" || (view.skipRefine && v.stage === "chosen");

async function act(promise, keepViewer) {
  try {
    const o = await promise;
    if (o && o.groups) show(o);
    else await refresh();
  } catch (e) {
    alert(e.message);
    await refresh();
  }
  if (!keepViewer) return;
  viewer.reload();
}

const choose = (v) => api(`/api/review/${enc(v.name)}/choose`, { method: "POST" });
const discard = (v) => api(`/api/review/${enc(v.name)}/discard`, { method: "POST" });

function button(text, title, onClick, cls) {
  const b = Object.assign(document.createElement("button"), { type: "button", textContent: text, title });
  if (cls) b.className = cls;
  b.addEventListener("click", onClick);
  return b;
}
function link(text, href, cls = "button") {
  return Object.assign(document.createElement("a"), { className: cls, href, textContent: text });
}

// One version: thumbnail (plays on hover), template, stage and actions.
function card(group, v, discarded) {
  const el = document.createElement("div");
  el.className = `vcard ${discarded ? "discarded" : v.stage}`;
  const video = document.createElement("video");
  video.src = videoURL(v, discarded) + `#t=${Math.min(v.duration * 0.6, 8).toFixed(1)}`;
  video.preload = "metadata";
  video.muted = true;
  video.loop = true;
  video.playsInline = true;
  video.addEventListener("mouseenter", () => video.play().catch(() => {}));
  video.addEventListener("mouseleave", () => video.pause());
  if (!discarded) video.addEventListener("click", () => viewer.open(group, v));
  const info = document.createElement("div");
  info.className = "vinfo";
  info.append(
    Object.assign(document.createElement("strong"), { textContent: tplName(v.template) }),
    Object.assign(document.createElement("span"), { className: "badge " + (discarded ? "discarded" : v.stage), textContent: discarded ? "Descartada" : STAGES[v.stage] ?? v.stage }),
    Object.assign(document.createElement("span"), { className: "meta", textContent: fmtDur(v.duration) }),
  );
  const title = Object.assign(document.createElement("div"), { className: "vtitle", textContent: v.title, title: v.description });
  const actions = document.createElement("div");
  actions.className = "vactions";
  if (discarded) {
    actions.append(button("Restaurar", "Vuelve como generada", () => act(api(`/api/review/${enc(v.name)}/restore`, { method: "POST" }))));
  } else {
    actions.append(button("▶", "Ver en pantalla completa", () => viewer.open(group, v)));
    if (v.stage === "generated") actions.append(button("Elegir", "Quedarse con esta para su idioma", () => act(choose(v)), "primary"));
    if (v.stage !== "generated") actions.append(link(v.stage === "refined" ? "Refinar de nuevo" : "Refinar", refineURL(v), "button" + (v.stage === "chosen" ? " primary" : "")));
    if (v.stage !== "generated") actions.append(button("Textos", "Título, descripción y tags", () => editTexts(el, v)));
    if (canQueue(v) && view.publishing) {
      actions.append(button("A la cola", "Pasa a la carpeta de publicación", () => act(api(`/api/review/${enc(v.name)}/queue`, { method: "POST" })), "primary"));
    }
    actions.append(button("Descartar", "", () => act(discard(v))));
  }
  el.append(video, info, title, actions);
  return el;
}

function editTexts(el, v) {
  const open = el.querySelector(".edit");
  if (open) {
    open.remove();
    return;
  }
  const box = document.createElement("div");
  box.className = "edit";
  const title = Object.assign(document.createElement("input"), { type: "text", value: v.title, placeholder: "Título" });
  const desc = Object.assign(document.createElement("textarea"), { rows: 4, value: v.description, placeholder: "Descripción" });
  const tags = Object.assign(document.createElement("input"), { type: "text", value: (v.tags ?? []).join(", "), placeholder: "Tags, separados por comas" });
  box.append(title, desc, tags, button("Guardar", "", () => act(api(`/api/review/${enc(v.name)}/meta`, {
    method: "PUT", json: { title: title.value, description: desc.value, tags: tags.value.split(",").map((t) => t.trim()).filter(Boolean) },
  })), "primary"));
  el.append(box);
  title.focus();
}

function groupBox(g) {
  const box = document.createElement("section");
  box.className = "group";
  const head = document.createElement("header");
  head.append(Object.assign(document.createElement("h3"), { textContent: g.source }));
  if (g.pending) head.append(Object.assign(document.createElement("span"), { className: "pending", textContent: `${g.pending} versiones generándose…` }));
  box.append(head);
  const langs = [...new Set(g.versions.map((v) => v.lang || "—"))];
  for (const lang of langs) {
    const row = document.createElement("div");
    row.className = "lang-row";
    const vs = g.versions.filter((v) => (v.lang || "—") === lang);
    const chosen = vs.find((v) => v.stage !== "generated");
    row.append(Object.assign(document.createElement("div"), { className: "lang", textContent: lang.toUpperCase() + (chosen ? " ✓" : "") }));
    const list = document.createElement("div");
    list.className = "versions";
    list.append(...vs.map((v) => card(g, v, false)));
    row.append(list);
    box.append(row);
  }
  if (!g.versions.length && !g.pending) box.append(Object.assign(document.createElement("p"), { className: "hint", textContent: "No quedan versiones." }));
  if (g.discarded.length) {
    const det = document.createElement("details");
    det.append(Object.assign(document.createElement("summary"), { textContent: `Descartadas (${g.discarded.length})` }));
    const list = document.createElement("div");
    list.className = "versions";
    list.append(...g.discarded.map((v) => card(g, v, true)));
    det.append(list);
    box.append(det);
  }
  return box;
}

function show(o) {
  view = o;
  $("refineRequired").checked = !o.skipRefine;
  const all = o.groups.flatMap((g) => g.versions);
  const count = (s) => all.filter((v) => v.stage === s).length;
  $("summary").textContent = `${count("generated")} para elegir · ${count("chosen")} para refinar · ${count("refined")} listas para la cola`;
  $("dirInfo").textContent = `Carpeta: ${o.generated} (las descartadas, en descartados/)`;
  // Keep the open details and anything being edited.
  const opened = new Set([...document.querySelectorAll(".group details[open]")].map((d) => d.closest(".group").querySelector("h3").textContent));
  if (document.querySelector("#groups .edit")) return;
  $("groups").replaceChildren(...(o.groups.length ? o.groups.map(groupBox) : [Object.assign(document.createElement("p"), { className: "none-yet", textContent: "Todavía no hay versiones: dejá fotos o videos en la carpeta de entrada." })]));
  for (const d of document.querySelectorAll(".group details")) if (opened.has(d.closest(".group").querySelector("h3").textContent)) d.open = true;
}

async function refresh() {
  try {
    show(await api("/api/review"));
  } catch (e) {
    $("groups").textContent = "No se pudo leer: " + e.message;
  }
}

$("refineRequired").addEventListener("input", async () => {
  try {
    const o = await api("/api/auto");
    await api("/api/auto/settings", { method: "PUT", json: { ...o.settings, skipRefine: !$("refineRequired").checked } });
  } catch (e) {
    alert(e.message);
  }
  refresh();
});
$("refresh").addEventListener("click", refresh);

// ---------- full-screen viewer ----------

const viewer = (() => {
  let group = null, list = [], i = 0;
  const video = $("vVideo");

  function render() {
    const v = list[i];
    if (!v) return close();
    video.src = videoURL(v, false);
    video.play().catch(() => {});
    $("vPos").textContent = `${group.source} · ${i + 1} / ${list.length}`;
    $("vTitle").textContent = v.title;
    $("vInfo").textContent = `${tplName(v.template)} · ${(v.lang || "").toUpperCase()} · ${STAGES[v.stage] ?? v.stage} · ${fmtDur(v.duration)}`;
    $("vDesc").textContent = v.description + (v.tags?.length ? "\n" + v.tags.map((t) => "#" + t).join(" ") : "");
    $("vChoose").hidden = v.stage !== "generated";
    $("vRefine").hidden = v.stage === "generated";
    $("vRefine").href = refineURL(v);
  }
  function open(g, v) {
    group = g;
    list = g.versions.slice();
    i = Math.max(0, list.findIndex((x) => x.name === v.name));
    $("viewer").hidden = false;
    render();
  }
  function close() {
    video.pause();
    video.removeAttribute("src");
    $("viewer").hidden = true;
    if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  }
  const go = (d) => {
    if (!list.length) return;
    i = (i + d + list.length) % list.length;
    render();
  };
  // After choosing or discarding: the list changed on the server.
  function reload() {
    if ($("viewer").hidden) return;
    const current = list[i]?.name;
    const g = view.groups.find((x) => x.source === group.source);
    if (!g || !g.versions.length) return close();
    group = g;
    list = g.versions.slice();
    const at = list.findIndex((x) => x.name === current);
    // Next one still to choose, if the current one is gone or decided.
    const pendingFrom = (k) => list.findIndex((x, n) => n >= k && x.stage === "generated");
    if (at < 0) i = Math.max(0, pendingFrom(0));
    else if (list[at].stage !== "generated" && pendingFrom(0) >= 0) i = pendingFrom(0);
    else i = at;
    render();
  }
  async function doChoose() {
    const v = list[i];
    if (v?.stage === "generated") await act(choose(v), true);
  }
  async function doDiscard() {
    const v = list[i];
    if (v) await act(discard(v), true);
  }
  $("vPrev").addEventListener("click", () => go(-1));
  $("vNext").addEventListener("click", () => go(1));
  $("vClose").addEventListener("click", close);
  $("vChoose").addEventListener("click", doChoose);
  $("vDiscard").addEventListener("click", doDiscard);
  document.addEventListener("keydown", (e) => {
    if ($("viewer").hidden || e.target.matches("input, textarea")) return;
    const k = e.key;
    if (k === "ArrowRight") go(1);
    else if (k === "ArrowLeft") go(-1);
    else if (k === "Escape") close();
    else if (k === "Enter") doChoose();
    else if (k === "x" || k === "X" || k === "Delete") doDiscard();
    else if (k === " ") video.paused ? video.play() : video.pause();
    else if (k === "f" || k === "F") (document.fullscreenElement ? document.exitFullscreen() : $("viewer").requestFullscreen()).catch(() => {});
    else return;
    e.preventDefault();
  });
  return { open, close, reload };
})();

refresh();
setInterval(() => { if (!document.hidden && $("viewer").hidden) refresh(); }, 10000);
