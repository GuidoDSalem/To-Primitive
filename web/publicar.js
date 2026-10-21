"use strict";

// Publishing page: the rule (when), the queue (what, in which order) and
// what was already published. Everything lives on the server, in the
// publishing folder itself; this page only shows it and sends changes.

const $ = (id) => document.getElementById(id);
const DAY_NAMES = ["domingo", "lunes", "martes", "miércoles", "jueves", "viernes", "sábado"];
const ZONES = [
  "America/Argentina/Buenos_Aires", "America/Montevideo", "America/Santiago", "America/Sao_Paulo",
  "America/Bogota", "America/Lima", "America/Mexico_City", "America/New_York", "America/Los_Angeles",
  "Europe/Madrid", "Europe/London", "UTC",
];

let view = null; // last overview from the server
let ruleDirty = false; // the rule form has unsaved changes

async function api(path, opts = {}) {
  if (opts.json !== undefined) {
    opts = { ...opts, headers: { "Content-Type": "application/json" }, body: JSON.stringify(opts.json) };
  }
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error((await res.text()) || res.statusText);
  return res.json();
}

const enc = encodeURIComponent;
const fmtSize = (b) => (b / 1e6).toFixed(1) + " MB";
const fmtDur = (s) => `${Math.floor(s / 60)}:${String(Math.round(s % 60)).padStart(2, "0")}`;

// A date in the rule's time zone: "martes 20/10 19:00".
function fmtWhen(iso) {
  const tz = view?.settings.tz || undefined;
  const d = new Date(iso);
  const parts = new Intl.DateTimeFormat("es-AR", { timeZone: tz, weekday: "long", day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).formatToParts(d);
  const get = (t) => parts.find((p) => p.type === t)?.value ?? "";
  return `${get("weekday")} ${get("day")}/${get("month")} ${get("hour")}:${get("minute")}`;
}

// "en 3 h", "en 2 días"...
function fmtIn(iso) {
  const m = Math.round((new Date(iso) - new Date(view.now)) / 60000);
  if (m < 60) return `en ${Math.max(0, m)} min`;
  if (m < 48 * 60) return `en ${Math.round(m / 60)} h`;
  return `en ${Math.round(m / 1440)} días`;
}

// ---------- the rule ----------

function timeRow(value) {
  const row = document.createElement("div");
  row.className = "row";
  const input = Object.assign(document.createElement("input"), { type: "time", value, required: true });
  input.addEventListener("input", () => { ruleDirty = true; });
  const del = Object.assign(document.createElement("button"), { type: "button", textContent: "✕", title: "Quitar" });
  del.addEventListener("click", () => {
    if ($("times").children.length > 1) row.remove();
    ruleDirty = true;
  });
  row.append(input, del);
  return row;
}

function showRule(s) {
  $("enabled").checked = s.enabled;
  $("times").replaceChildren(...s.times.map(timeRow));
  for (const b of $("days").children) b.classList.toggle("on", s.days.includes(+b.dataset.value));
  const zones = ZONES.includes(s.tz) ? ZONES : [s.tz, ...ZONES];
  $("tz").replaceChildren(...zones.map((z) => new Option(z.replace(/_/g, " "), z, false, z === s.tz)));
  ruleDirty = false;
}

function readRule() {
  return {
    ...view?.settings, // the platforms are saved on their own
    enabled: $("enabled").checked,
    times: [...$("times").querySelectorAll("input")].map((i) => i.value).filter(Boolean),
    days: [...$("days").children].filter((b) => b.classList.contains("on")).map((b) => +b.dataset.value),
    tz: $("tz").value,
  };
}

$("addTime").addEventListener("click", () => {
  $("times").append(timeRow("12:00"));
  ruleDirty = true;
});
$("days").addEventListener("click", (e) => {
  const b = e.target.closest("button");
  if (!b) return;
  b.classList.toggle("on");
  ruleDirty = true;
});
for (const id of ["enabled", "tz"]) $(id).addEventListener("input", () => { ruleDirty = true; });

$("saveRule").addEventListener("click", async () => {
  try {
    show(await api("/api/publish/settings", { method: "PUT", json: readRule() }), true);
    $("ruleStatus").textContent = "Regla guardada.";
  } catch (e) {
    $("ruleStatus").textContent = "Error: " + e.message;
  }
});

// ---------- queue and published ----------

async function act(promise) {
  try {
    show(await promise);
  } catch (e) {
    alert(e.message);
    refresh();
  }
}

function moveTo(name, delta) {
  const names = view.queue.map((it) => it.name);
  const i = names.indexOf(name), j = i + delta;
  if (j < 0 || j >= names.length) return;
  [names[i], names[j]] = [names[j], names[i]];
  act(api("/api/publish/order", { method: "PUT", json: { names } }));
}

function button(text, title, onClick) {
  const b = Object.assign(document.createElement("button"), { type: "button", textContent: text, title });
  b.addEventListener("click", onClick);
  return b;
}

// The video file as it is (to upload it by hand, send it, keep it…).
function downloadLink(it, published) {
  return Object.assign(document.createElement("a"), {
    className: "button", textContent: "Descargar", title: "Bajar el MP4", download: it.name,
    href: `/api/publish/${published ? "published" : "queue"}/${enc(it.name)}/video?download=1&v=${it.version}`,
  });
}

// A discarded video: to see it, download it or put it back in the queue.
function discardedCard(it) {
  const card = document.createElement("div");
  card.className = "item gone";
  const url = `/api/publish/discarded/${enc(it.name)}/video?v=${it.version}`;
  const video = Object.assign(document.createElement("video"), { src: url + "#t=1", preload: "metadata", muted: true, playsInline: true, title: "Ver" });
  video.addEventListener("click", () => window.open(url, "_blank"));
  const body = document.createElement("div");
  body.className = "body";
  body.append(Object.assign(document.createElement("div"), { className: "title", textContent: it.title }),
    Object.assign(document.createElement("div"), { className: "file", textContent: `${it.name} · ${fmtDur(it.duration)} · ${fmtSize(it.size)}` }));
  const actions = document.createElement("div");
  actions.className = "actions";
  actions.append(
    button("Restaurar", "Vuelve a la cola", () => act(api(`/api/publish/discarded/${enc(it.name)}/restore`, { method: "POST" }))),
    Object.assign(document.createElement("a"), { className: "button", textContent: "Descargar", download: it.name, href: url.replace("?", "?download=1&") }),
  );
  card.append(Object.assign(document.createElement("div"), { className: "pos", textContent: "✕" }), video, body, actions);
  return card;
}

function itemCard(it, i, published) {
  const card = document.createElement("div");
  card.className = "item" + (it.skipped ? " skipped" : "");
  const pos = Object.assign(document.createElement("div"), { className: "pos", textContent: published ? "✓" : i + 1 });
  const video = document.createElement("video");
  video.src = `/api/publish/${published ? "published" : "queue"}/${enc(it.name)}/video?v=${it.version}#t=${Math.min(1, it.duration / 2).toFixed(1)}`;
  video.preload = "metadata";
  video.muted = true;
  video.playsInline = true;
  video.title = "Ver cómo se va a publicar";
  video.addEventListener("click", () => preview.open(published ? view.published : view.queue, i, published));

  const body = document.createElement("div");
  body.className = "body";
  const add = (cls, text) => body.append(Object.assign(document.createElement("div"), { className: cls, textContent: text }));
  add("title", it.title);
  if (published) {
    add("when", it.publishedAt ? "Publicado el " + fmtWhen(it.publishedAt) : "Publicado");
    if (it.result) add("file", it.result);
  } else if (it.skipped) {
    add("when none", "Salteado: queda en la carpeta, no se publica");
  } else if (it.scheduledAt) {
    add("when", `${fmtWhen(it.scheduledAt)} · ${fmtIn(it.scheduledAt)}`);
  } else {
    add("when none", view.settings.enabled ? "Sin horario" : "Regla pausada");
  }
  add("file", `${it.name} · ${fmtDur(it.duration)} · ${it.width}×${it.height} · ${fmtSize(it.size)}`);
  add("desc", it.description + (it.tags?.length ? "\n" + it.tags.map((t) => "#" + t.replace(/^#/, "")).join(" ") : ""));
  for (const w of it.warnings ?? []) add("warn", "⚠ " + w);
  if (it.publishing) add("when", "Publicando… (subiendo a las plataformas)");
  for (const [p, u] of Object.entries(it.uploads ?? {})) {
    const line = Object.assign(document.createElement("div"), { className: "file" });
    line.append(`${PLATFORM_NAMES[p] ?? p}: `);
    if (u.url) line.append(Object.assign(document.createElement("a"), { href: u.url, target: "_blank", rel: "noopener", textContent: u.url }));
    else line.append(`subido (${u.id})`);
    if (u.msg) line.append(` · ${u.msg}`);
    body.append(line);
  }

  const actions = document.createElement("div");
  actions.className = "actions";
  if (published) {
    actions.append(button("Volver a la cola", "Lo pone primero en la cola", () => act(api(`/api/publish/published/${enc(it.name)}/requeue`, { method: "POST" }))));
    actions.append(downloadLink(it, true));
  } else {
    const row = document.createElement("div");
    row.className = "row";
    row.append(
      button("↑", "Antes", () => moveTo(it.name, -1)),
      button("↓", "Después", () => moveTo(it.name, +1)),
    );
    actions.append(
      row,
      button(it.skipped ? "Incluir" : "Saltear", it.skipped ? "Vuelve a la cola" : "Queda en la carpeta sin publicarse",
        () => act(api(`/api/publish/queue/${enc(it.name)}/skip`, { method: "POST", json: { skip: !it.skipped } }))),
      ...(it.editable ? [
        Object.assign(document.createElement("a"), { className: "button", href: `reels.html?edit=${enc(it.name)}`, textContent: "Editar reel", title: "Vuelve al editor con todos sus ajustes (en todos sus idiomas de la cola)" }),
      ] : []),
      button("Textos", "Título, descripción y etiquetas", () => toggleEdit(card, it)),
      ...(it.duplicable ? [button("Duplicar en otro idioma", "Traduce los textos con el modelo local y vuelve a generar el reel", () => toggleDuplicate(card, it))] : []),
      downloadLink(it, false),
      Object.assign(button(it.publishing ? "Publicando…" : "Publicar ahora", "Revisar cómo sale y publicarlo sin esperar la regla", () => postDialog.open(it)),
        { disabled: !!it.publishing }),
      button("Descartar", "Sale de la cola y pasa a descartados/ (no se borra; se puede restaurar)", () => {
        if (confirm(`¿Sacar "${it.title}" de la cola? Pasa a descartados/ y lo podés restaurar.`)) act(api(`/api/publish/queue/${enc(it.name)}/discard`, { method: "POST" }));
      }),
    );
  }
  card.append(pos, video, body, actions);
  return card;
}

// The reel in another language: a local model translates the texts and the
// generator draws it again; it joins the queue right after this one.
const LANGS = { es: "Español", en: "English", pt: "Português", fr: "Français", it: "Italiano", de: "Deutsch" };
function toggleDuplicate(card, it) {
  const open = card.querySelector(".dup");
  if (open) {
    open.remove();
    return;
  }
  const box = document.createElement("div");
  box.className = "edit dup";
  const sel = document.createElement("select");
  const mine = (it.name.match(/ · ([a-z]{2})\.\w+$/) || [])[1];
  sel.append(...Object.entries(LANGS).filter(([c]) => c !== mine).map(([c, n]) => new Option(`${n} (${c})`, c)));
  sel.append(new Option("Otro…", ""));
  const go = button("Duplicar", "", async () => {
    let lang = sel.value;
    if (!lang) lang = (prompt("Código de dos letras del idioma (por ejemplo: ja, ko, ru):") || "").trim().toLowerCase();
    if (!lang) return;
    try {
      await api(`/api/publish/queue/${enc(it.name)}/duplicate`, { method: "POST", json: { lang } });
      box.replaceChildren(Object.assign(document.createElement("span"), { className: "hint", textContent: `Traduciendo y generando en ${lang.toUpperCase()}… va a aparecer en la cola, después de este.` }));
      refreshCopies();
    } catch (e) {
      alert(e.message);
    }
  });
  go.className = "primary";
  box.append(Object.assign(document.createElement("span"), { className: "hint", textContent: "Los textos (en pantalla, título, descripción y tags) los traduce el modelo local." }), sel, go);
  card.querySelector(".body").append(box);
}

// Copies in other languages on their way to the queue.
async function refreshCopies() {
  let jobs = [];
  try {
    jobs = await api("/api/publish/copies");
  } catch { /* the generator may be off */ }
  $("copies").hidden = !jobs.length;
  $("copiesList").replaceChildren(...jobs.map((j) => {
    const row = document.createElement("div");
    row.className = "job " + j.state;
    row.append(
      Object.assign(document.createElement("div"), { className: "what", textContent: `${j.from} → ${j.lang.toUpperCase()}` }),
      Object.assign(document.createElement("div"), { className: "state", textContent: j.state === "error" ? "Falló" : j.state === "running" ? j.stage || "Generando" : "En espera" }),
    );
    if (j.state === "running") row.append(Object.assign(document.createElement("progress"), { max: 1, value: j.progress || 0 }));
    if (j.state === "error") {
      row.append(Object.assign(document.createElement("div"), { className: "err", textContent: j.error }));
      row.append(button("Reintentar", "", async () => {
        await api(`/api/auto/jobs/${j.id}/retry`, { method: "POST" }).catch((e) => alert(e.message));
        refreshCopies();
      }));
    }
    return row;
  }));
  // Something finished: show it in the queue.
  const busy = jobs.some((j) => j.state !== "error");
  if (refreshCopies.busy && !busy) refresh();
  refreshCopies.busy = busy;
}

// Title, description and tags, saved in the JSON next to the video.
function toggleEdit(card, it) {
  const open = card.querySelector(".edit");
  if (open) {
    open.remove();
    return;
  }
  const box = document.createElement("div");
  box.className = "edit";
  const title = Object.assign(document.createElement("input"), { type: "text", value: it.title, placeholder: "Título" });
  const desc = Object.assign(document.createElement("textarea"), { rows: 3, value: it.description, placeholder: "Descripción (incluí #Shorts)" });
  const tags = Object.assign(document.createElement("input"), { type: "text", value: (it.tags ?? []).join(", "), placeholder: "Etiquetas, separadas por comas" });
  const save = button("Guardar", "", () => act(api(`/api/publish/queue/${enc(it.name)}/meta`, {
    method: "PUT",
    json: { title: title.value, description: desc.value, tags: tags.value.split(",").map((t) => t.trim()).filter(Boolean) },
  })));
  save.className = "primary";
  box.append(title, desc, tags, save);
  card.querySelector(".body").append(box);
  title.focus();
}

function show(o, ruleToo = false) {
  view = o;
  if (ruleToo || !ruleDirty) showRule(o.settings);
  const s = o.settings;
  const days = s.days.length === 7 ? "todos los días" : s.days.slice().sort().map((d) => DAY_NAMES[d]).join(", ");
  $("nextInfo").className = "next" + (s.enabled ? "" : " off");
  $("nextInfo").textContent = s.enabled
    ? `Regla activa: ${days} a las ${s.times.join(", ")} (${s.tz.replace(/_/g, " ")}).` + (o.nextSlot ? ` Próxima publicación: ${fmtWhen(o.nextSlot)} (${fmtIn(o.nextSlot)}).` : "")
    : "Regla pausada: nada se publica solo. Activala a la izquierda, o publicá a mano.";
  $("dirInfo").textContent = `Carpeta: ${o.dir}`;
  const plats = (s.platforms ?? []).map((p) => PLATFORM_NAMES[p] ?? p);
  $("uploaderInfo").textContent = plats.length
    ? `Publica en ${plats.join(" y ")}.`
    : "Publicación simulada: marca el video y lo mueve a publicados/, sin subirlo a ningún lado. Elegí plataformas arriba.";
  drawPlatforms();

  const active = o.queue.filter((it) => !it.skipped).length;
  $("queueCount").textContent = `${active}` + (o.queue.length > active ? ` (+${o.queue.length - active} salteados)` : "");
  $("pubCount").textContent = o.published.length;
  $("discardedBox").hidden = !o.discarded.length;
  $("discCount").textContent = o.discarded.length;
  $("discarded").replaceChildren(...o.discarded.map(discardedCard));
  // Don't redraw the queue while a title or description in it is being edited.
  const typing = $("queue").contains(document.activeElement) && document.activeElement.matches("input, textarea");
  if (!typing) {
    $("queue").replaceChildren(...(o.queue.length ? o.queue.map((it, i) => itemCard(it, i, false)) : [Object.assign(document.createElement("p"), { className: "none-yet", textContent: "La carpeta no tiene videos para publicar." })]));
  }
  $("published").replaceChildren(...(o.published.length ? o.published.map((it, i) => itemCard(it, i, true)) : [Object.assign(document.createElement("p"), { className: "none-yet", textContent: "Todavía no se publicó nada." })]));

  $("log").replaceChildren(...o.log.slice(0, 25).map((e) => {
    const li = document.createElement("li");
    if (e.action === "error") li.className = "error";
    const strong = Object.assign(document.createElement("strong"), { textContent: e.action });
    li.append(`${fmtWhen(e.time)} · `, strong, e.name ? ` ${e.name}` : "", e.detail ? ` — ${e.detail}` : "");
    return li;
  }));
}

// ---------- preview: what is going to be published ----------

const preview = (() => {
  let list = [], i = 0, published = false;
  const video = $("pvVideo");
  // The channel name for the mock-up: the brand of the last reel, if any.
  let channel = "@tucanal";
  function render() {
    const it = list[i];
    if (!it) return close();
    video.src = `/api/publish/${published ? "published" : "queue"}/${enc(it.name)}/video?v=${it.version}`;
    video.play().catch(() => {});
    const tags = (it.tags ?? []).map((t) => "#" + t.replace(/^#/, ""));
    $("pvPos").textContent = `${published ? "Publicados" : "Cola"} · ${i + 1} / ${list.length}`;
    $("pvTitle").textContent = it.title;
    $("pvWhen").textContent = published
      ? (it.publishedAt ? "Publicado el " + fmtWhen(it.publishedAt) : "Publicado")
      : it.skipped ? "Salteado: no se publica" : it.scheduledAt ? `Sale el ${fmtWhen(it.scheduledAt)} (${fmtIn(it.scheduledAt)})` : "Sin horario (regla pausada)";
    $("pvDesc").textContent = it.description;
    $("pvTags").textContent = tags.join(" ");
    $("pvWarn").textContent = (it.warnings ?? []).map((w) => "⚠ " + w).join("\n");
    $("pvFile").textContent = `${it.name} · ${fmtDur(it.duration)} · ${it.width}×${it.height} · ${fmtSize(it.size)}`;
    // As the apps show it: title (Shorts uses it as caption) and the first line of the description.
    const first = it.description.split("\n").find((l) => l.trim()) ?? "";
    $("pvCaption").replaceChildren(
      Object.assign(document.createElement("div"), { className: "cap-title", textContent: it.title }),
      ...(first && first !== it.title ? [Object.assign(document.createElement("div"), { className: "cap-desc", textContent: first })] : []),
    );
    $("pvChannel").textContent = it.brand || channel;
  }
  function open(items, at, isPublished) {
    list = items.slice();
    i = at;
    published = isPublished;
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
    i = (i + d + list.length) % list.length;
    render();
  };
  $("pvShowUI").addEventListener("input", () => { $("pvShorts").hidden = !$("pvShowUI").checked; });
  $("pvPrev").addEventListener("click", () => go(-1));
  $("pvNext").addEventListener("click", () => go(1));
  $("pvClose").addEventListener("click", close);
  video.addEventListener("click", () => (video.paused ? video.play() : video.pause()));
  document.addEventListener("keydown", (e) => {
    if ($("viewer").hidden || e.target.matches("input[type=text], textarea")) return;
    if (e.key === "ArrowRight") go(1);
    else if (e.key === "ArrowLeft") go(-1);
    else if (e.key === "Escape") close();
    else if (e.key === " ") video.paused ? video.play() : video.pause();
    else if (e.key === "f" || e.key === "F") (document.fullscreenElement ? document.exitFullscreen() : $("viewer").requestFullscreen()).catch(() => {});
    else return;
    e.preventDefault();
  });
  return { open, close, setChannel: (c) => { if (c) channel = c; } };
})();

async function refresh() {
  try {
    show(await api("/api/publish"));
  } catch (e) {
    $("nextInfo").textContent = "No se pudo leer la cola: " + e.message;
  }
}
$("refresh").addEventListener("click", refresh);
refresh();
setInterval(() => { if (!document.hidden && $("viewer").hidden) refresh(); }, 20000);
refreshCopies();
setInterval(() => { if (!document.hidden) refreshCopies(); }, 4000);

// ---------- platforms: connected accounts and where to publish ----------

const PLATFORM_NAMES = { youtube: "YouTube", tiktok: "TikTok" };
const PRIVACY = {
  youtube: [["public", "Público"], ["unlisted", "No listado"], ["private", "Privado"]],
  tiktok: [["PUBLIC_TO_EVERYONE", "Público"], ["FOLLOWER_OF_CREATOR", "Seguidores"], ["MUTUAL_FOLLOW_FRIENDS", "Amigos"], ["SELF_ONLY", "Solo yo"]],
};
let accounts = [];

async function loadAccounts() {
  try {
    accounts = await api("/api/publish/accounts");
  } catch (e) {
    $("platStatus").textContent = "No se pudieron leer las cuentas: " + e.message;
  }
  drawPlatforms();
}

async function savePlatforms(change) {
  try {
    show(await api("/api/publish/settings", { method: "PUT", json: { ...view.settings, ...change } }));
    $("platStatus").textContent = "Guardado.";
  } catch (e) {
    $("platStatus").textContent = "Error: " + e.message;
  }
}

function drawPlatforms() {
  if (!view) return;
  const s = view.settings;
  const on = new Set(s.platforms ?? []);
  $("platforms").replaceChildren(...accounts.map((a) => {
    const box = document.createElement("div");
    box.className = "platform" + (a.connected ? " connected" : "");
    const head = document.createElement("div");
    head.className = "head";
    const use = Object.assign(document.createElement("input"), { type: "checkbox", checked: on.has(a.id), disabled: !a.connected && !on.has(a.id) });
    use.addEventListener("input", () => {
      const next = new Set(on);
      use.checked ? next.add(a.id) : next.delete(a.id);
      savePlatforms({ platforms: accounts.map((x) => x.id).filter((id) => next.has(id)) });
    });
    const label = document.createElement("label");
    label.className = "check";
    label.append(use, " Publicar en ", Object.assign(document.createElement("strong"), { textContent: a.name }));
    head.append(label);
    const state = document.createElement("div");
    state.className = "hint";
    if (!a.configured) {
      state.textContent = `Falta configurar: ${a.missing} en el .env (ver README).`;
    } else if (a.connected) {
      state.textContent = `Conectado${a.account ? " como " + a.account : ""}.`;
    } else {
      state.textContent = "Sin conectar.";
    }
    const actions = document.createElement("div");
    actions.className = "row";
    if (a.configured && !a.connected) {
      actions.append(Object.assign(document.createElement("a"), { className: "button primary", href: `/api/publish/accounts/${a.id}/connect`, textContent: `Conectar ${a.name}` }));
    }
    if (a.connected) {
      const field = PRIVACY_FIELD[a.id];
      const sel = document.createElement("select");
      sel.title = "Privacidad con la que se publica";
      sel.append(...PRIVACY[a.id].map(([v, t]) => new Option(t, v, false, (s[field] || PRIVACY[a.id][0][0]) === v)));
      sel.addEventListener("input", () => savePlatforms({ [field]: sel.value }));
      actions.append(sel, button("Desconectar", "Olvida el permiso guardado", async () => {
        if (!confirm(`¿Desconectar ${a.name}?`)) return;
        accounts = await api(`/api/publish/accounts/${a.id}/disconnect`, { method: "POST" });
        if (on.has(a.id)) await savePlatforms({ platforms: [...on].filter((p) => p !== a.id) });
        drawPlatforms();
      }));
    }
    box.append(head, state, actions);
    if (a.configured && !a.connected) {
      const uri = document.createElement("p");
      uri.className = "hint";
      uri.append("Dirección de retorno a registrar: ", Object.assign(document.createElement("code"), { textContent: a.redirect }));
      box.append(uri);
    }
    if (a.connected && on.has(a.id) && a.id === "tiktok") {
      box.append(Object.assign(document.createElement("p"), { className: "hint", textContent: "Mientras TikTok no apruebe (audite) la app, solo deja publicar en privado: se publica como \"Solo yo\" y después lo cambiás en la app." }));
    }
    return box;
  }));
}
const PRIVACY_FIELD = { youtube: "youtubePrivacy", tiktok: "tiktokPrivacy" };

loadAccounts();
{
  const just = new URLSearchParams(location.search).get("conectado");
  if (just) {
    $("platStatus").textContent = `${PLATFORM_NAMES[just] ?? just} conectado ✓. Marcá "Publicar en ${PLATFORM_NAMES[just] ?? just}" para usarlo.`;
    history.replaceState(null, "", "publicar.html");
  }
}

// ---------- confirming a post: where, how, with which settings ----------

const MUSIC_URL = "https://www.tiktok.com/legal/page/global/music-usage-confirmation/en";
const BRANDED_URL = "https://www.tiktok.com/legal/page/global/bc-policy/en";
const TT_PRIVACY = { PUBLIC_TO_EVERYONE: "Todos", FOLLOWER_OF_CREATOR: "Seguidores", MUTUAL_FOLLOW_FRIENDS: "Amigos", SELF_ONLY: "Solo yo" };

const postDialog = (() => {
  const dlg = $("postDialog");
  let item = null, info = null;
  const state = { platforms: new Set(), yt: {}, tt: {} };
  const el = (tag, props = {}, ...kids) => {
    const e = Object.assign(document.createElement(tag), props);
    e.append(...kids);
    return e;
  };
  const check = (label, checked, onInput, disabled = false, note = "") => {
    const box = el("input", { type: "checkbox", checked, disabled });
    box.addEventListener("input", () => onInput(box.checked));
    return el("label", { className: "check" + (disabled ? " off" : "") }, box, " " + label, note ? el("span", { className: "kinds", textContent: " " + note }) : "");
  };

  async function open(it) {
    item = it;
    info = null;
    $("postTitle").textContent = it.title;
    $("postVideo").src = `/api/publish/queue/${enc(it.name)}/video?v=${it.version}`;
    $("postBody").replaceChildren(el("p", { className: "hint", textContent: "Leyendo las cuentas…" }));
    $("postPlatforms").replaceChildren();
    $("postStatus").textContent = "";
    $("postGo").disabled = true;
    dlg.showModal();
    try {
      info = await api(`/api/publish/queue/${enc(it.name)}/post`);
    } catch (e) {
      $("postBody").replaceChildren(el("p", { className: "warn-line", textContent: "No se pudo preparar: " + e.message }));
      return;
    }
    const connected = (info.accounts ?? []).filter((a) => a.connected).map((a) => a.id);
    state.platforms = new Set((info.platforms ?? []).filter((p) => connected.includes(p)));
    state.yt = { privacy: info.youtube.privacy || "public", title: info.youtube.title };
    // TikTok: nothing chosen for the person (privacy and interactions start empty).
    state.tt = { privacy: "", caption: info.tiktok?.caption ?? "", allowComment: false, allowDuet: false, allowStitch: false, disclose: false, brandOrganic: false, brandContent: false };
    $("postPlatforms").replaceChildren(...(info.accounts ?? []).map((a) =>
      check(`Publicar en ${a.name}`, state.platforms.has(a.id), (on) => {
        on ? state.platforms.add(a.id) : state.platforms.delete(a.id);
        render();
      }, !a.connected, a.connected ? "" : "(sin conectar)")));
    render();
  }

  function youtubeBox() {
    const acc = info.accounts.find((a) => a.id === "youtube");
    const title = el("input", { type: "text", value: state.yt.title, maxLength: 100 });
    title.addEventListener("input", () => { state.yt.title = title.value; validate(); });
    const privacy = el("select");
    privacy.append(...PRIVACY.youtube.map(([v, t]) => new Option(t, v, false, v === state.yt.privacy)));
    privacy.addEventListener("input", () => { state.yt.privacy = privacy.value; });
    return el("section", { className: "post-box" },
      el("h3", { textContent: "YouTube" }, el("span", { className: "kinds", textContent: acc?.account ? " · " + acc.account : "" })),
      el("label", { textContent: "Título" }), title,
      el("label", { textContent: "Visibilidad" }), privacy);
  }

  function tiktokBox() {
    const tt = info.tiktok;
    const box = el("section", { className: "post-box" });
    if (!tt || tt.error || !tt.creator) {
      box.append(el("h3", { textContent: "TikTok" }), el("p", { className: "warn-line", textContent: "No se pudo leer la cuenta de TikTok" + (tt?.error ? ": " + tt.error : "") + ". Probá más tarde." }));
      return box;
    }
    const c = tt.creator;
    const who = el("div", { className: "post-creator" });
    if (c.creator_avatar_url) who.append(el("img", { src: c.creator_avatar_url, alt: "" }));
    who.append(el("div", {}, el("strong", { textContent: c.creator_nickname || c.creator_username }), el("div", { className: "kinds", textContent: "@" + c.creator_username })));
    box.append(el("h3", { textContent: "TikTok" }), who);
    if (c.max_video_post_duration_sec && item.duration > c.max_video_post_duration_sec) {
      box.append(el("p", { className: "warn-line", textContent: `Este video dura ${fmtDur(item.duration)} y la cuenta permite hasta ${fmtDur(c.max_video_post_duration_sec)}.` }));
    }
    const caption = el("textarea", { rows: 4, value: state.tt.caption, maxLength: 2200 });
    const count = el("span", { className: "kinds" });
    const upd = () => { count.textContent = ` ${[...caption.value].length} / 2200`; };
    caption.addEventListener("input", () => { state.tt.caption = caption.value; upd(); validate(); });
    upd();
    const privacy = el("select", { required: true });
    privacy.append(new Option("Elegí quién puede verlo…", "", true, state.tt.privacy === ""));
    for (const v of c.privacy_level_options ?? []) {
      const o = new Option(TT_PRIVACY[v] ?? v, v, false, v === state.tt.privacy);
      if (v === "SELF_ONLY" && state.tt.brandContent) {
        o.disabled = true;
        o.textContent += " (no disponible con contenido de marca)";
      }
      privacy.append(o);
    }
    privacy.addEventListener("input", () => { state.tt.privacy = privacy.value; render(); });
    const inter = el("div", { className: "post-inter" },
      check("Permitir comentarios", state.tt.allowComment, (v) => { state.tt.allowComment = v; }, c.comment_disabled, c.comment_disabled ? "(desactivado en la cuenta)" : ""),
      check("Permitir Dúo", state.tt.allowDuet, (v) => { state.tt.allowDuet = v; }, c.duet_disabled, c.duet_disabled ? "(desactivado en la cuenta)" : ""),
      check("Permitir Stitch", state.tt.allowStitch, (v) => { state.tt.allowStitch = v; }, c.stitch_disabled, c.stitch_disabled ? "(desactivado en la cuenta)" : ""));
    const disclose = el("div", { className: "post-disclose" },
      check("Divulgar contenido comercial", state.tt.disclose, (v) => {
        state.tt.disclose = v;
        if (!v) state.tt.brandOrganic = state.tt.brandContent = false;
        render();
      }));
    if (state.tt.disclose) {
      disclose.append(
        check("Tu marca", state.tt.brandOrganic, (v) => { state.tt.brandOrganic = v; render(); }, false, "— se etiqueta como \"Contenido promocional\""),
        check("Contenido de marca", state.tt.brandContent, (v) => {
          state.tt.brandContent = v;
          if (v && state.tt.privacy === "SELF_ONLY") state.tt.privacy = "";
          render();
        }, false, "— se etiqueta como \"Colaboración pagada\""));
      if (!state.tt.brandOrganic && !state.tt.brandContent) disclose.append(el("p", { className: "warn-line", textContent: "Elegí si es tu marca, contenido de marca o ambos." }));
    }
    const policies = el("p", { className: "hint" }, "Al publicar, aceptás la ", el("a", { href: MUSIC_URL, target: "_blank", rel: "noopener", textContent: "Music Usage Confirmation" }), " de TikTok");
    if (state.tt.brandContent) policies.append(" y su ", el("a", { href: BRANDED_URL, target: "_blank", rel: "noopener", textContent: "Branded Content Policy" }));
    policies.append(". El video puede tardar unos minutos en aparecer en tu perfil.");
    box.append(el("label", { textContent: "Texto (descripción)" }), caption, count,
      el("label", { textContent: "Quién puede verlo" }), privacy, inter, disclose, policies);
    return box;
  }

  function render() {
    const parts = [];
    if (state.platforms.has("youtube")) parts.push(youtubeBox());
    if (state.platforms.has("tiktok")) parts.push(tiktokBox());
    if (!parts.length) parts.push(el("p", { className: "hint", textContent: "Sin plataformas marcadas: la publicación es simulada (se mueve a publicados/ sin subirse)." }));
    $("postBody").replaceChildren(...parts);
    validate();
  }

  function problem() {
    if (state.platforms.has("youtube") && !state.yt.title.trim()) return "Falta el título para YouTube.";
    if (state.platforms.has("tiktok")) {
      const tt = info.tiktok, c = tt?.creator;
      if (!c) return "No se pudo leer la cuenta de TikTok.";
      if (c.max_video_post_duration_sec && item.duration > c.max_video_post_duration_sec) return "El video es más largo de lo que permite la cuenta de TikTok.";
      if (!state.tt.privacy) return "Elegí quién puede verlo en TikTok.";
      if (state.tt.disclose && !state.tt.brandOrganic && !state.tt.brandContent) return "Completá el contenido comercial.";
    }
    return "";
  }

  function validate() {
    const p = problem();
    $("postGo").disabled = !!p;
    $("postStatus").className = "hint";
    $("postStatus").textContent = p;
  }

  async function go() {
    const choice = { platforms: [...state.platforms] };
    if (state.platforms.has("youtube")) choice.youtube = { privacy: state.yt.privacy, title: state.yt.title };
    if (state.platforms.has("tiktok")) {
      const t = state.tt;
      choice.tiktok = { privacy: t.privacy, caption: t.caption, allowComment: t.allowComment, allowDuet: t.allowDuet, allowStitch: t.allowStitch, brandOrganic: t.brandOrganic, brandContent: t.brandContent };
    }
    $("postGo").disabled = true;
    $("postCancel").disabled = true;
    $("postStatus").className = "hint";
    $("postStatus").textContent = "Publicando… (subiendo el video, puede tardar un poco)";
    try {
      show(await api(`/api/publish/queue/${enc(item.name)}/now`, { method: "POST", json: choice }));
      close();
    } catch (e) {
      $("postCancel").disabled = false;
      validate();
      // After validate(), which would clear it: the error stays in sight.
      $("postStatus").className = "post-error";
      $("postStatus").textContent = "No se publicó: " + e.message;
      refresh();
    }
  }

  function close() {
    $("postVideo").pause();
    $("postVideo").removeAttribute("src");
    $("postCancel").disabled = false;
    dlg.close();
  }
  $("postForm").addEventListener("submit", (e) => { e.preventDefault(); if (!problem()) go(); });
  $("postCancel").addEventListener("click", close);
  dlg.addEventListener("cancel", (e) => { if ($("postCancel").disabled) e.preventDefault(); else close(); });
  return { open };
})();
