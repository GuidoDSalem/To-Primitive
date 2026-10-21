"use strict";

// Live mode: webcam frames go to the server one at a time, whenever it asks
// for one ("ready"), so it always works on the freshest frame. Keyframes come
// back tagged with the capture time of their frame and are drawn interpolated
// (with Motion.render), slightly behind real time so that there is always a
// next keyframe to interpolate towards.
const Live = (() => {
  const el = (id) => document.getElementById(id);
  let s = null; // the running session
  const ema = (avg, x, k = 0.2) => (avg ? avg + (x - avg) * k : x);

  const options = (params) => ({ ...params, adapt: +el("liveAdapt").value, budget: +el("liveBudget").value, flow: el("liveFlow").checked });

  async function start(params) {
    if (s) return;
    if (!navigator.mediaDevices?.getUserMedia) throw new Error("este navegador no da acceso a la cámara");
    const stream = await navigator.mediaDevices.getUserMedia({ video: { width: { ideal: 640 }, height: { ideal: 480 } }, audio: false });
    const cam = el("liveCam");
    cam.srcObject = stream;
    await cam.play();
    const res = await fetch("/api/live", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(options(params)) });
    if (!res.ok) {
      stream.getTracks().forEach((t) => t.stop());
      throw new Error(await res.text());
    }
    const d = await res.json();
    s = {
      id: d.id, params, stream, cam, events: null, grab: document.createElement("canvas"),
      meta: null, kfs: [], sending: false, latency: 0, interval: 0, stats: null,
      raf: 0, rec: null, recStart: 0, paramTimer: 0,
    };
    el("liveView").hidden = false;
    el("liveSection").hidden = false;
    connect();
    s.raf = requestAnimationFrame(frame);
  }

  function stop() {
    if (!s) return;
    const cur = s;
    s = null;
    if (cur.rec?.state === "recording") cur.rec.stop();
    cancelAnimationFrame(cur.raf);
    cur.events?.close();
    cur.stream.getTracks().forEach((t) => t.stop());
    clearTimeout(cur.paramTimer);
    fetch(`/api/live/${cur.id}`, { method: "DELETE" }).catch(() => {});
    el("liveView").hidden = true;
    el("liveSection").hidden = true;
    el("liveRec").textContent = "● Grabar";
  }

  function setParams(params, restart = false) {
    if (!s) return;
    s.params = params;
    clearTimeout(s.paramTimer);
    const id = s.id;
    s.paramTimer = setTimeout(() => fetch(`/api/live/${id}/params`, {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...options(params), restart }),
    }).catch((e) => console.error(e)), restart ? 0 : 120);
  }

  function connect() {
    const es = new EventSource(`/api/live/${s.id}/events`);
    s.events = es;
    es.addEventListener("ready", () => capture());
    es.addEventListener("animation", (e) => {
      s.meta = JSON.parse(e.data);
      s.kfs = [];
    });
    es.addEventListener("keyframe", (e) => {
      const d = JSON.parse(e.data);
      if (d.reset) s.kfs = [];
      const last = s.kfs.at(-1);
      if (last) s.interval = ema(s.interval, d.t - last.t);
      s.latency = ema(s.latency, performance.now() - d.t);
      s.kfs.push({ t: d.t, shapes: d.shapes });
      if (s.kfs.length > 40) s.kfs.splice(0, s.kfs.length - 40);
    });
    es.addEventListener("stats", (e) => { s.stats = JSON.parse(e.data); showStats(); });
  }

  // Grab the current webcam frame at the working size and send it.
  function capture() {
    if (!s || s.sending) return;
    const { cam, grab } = s;
    const vw = cam.videoWidth, vh = cam.videoHeight;
    if (!vw || !vh) return;
    const scale = Math.min(1, (s.params.inputSize || 128) / Math.max(vw, vh));
    grab.width = Math.max(1, Math.round(vw * scale));
    grab.height = Math.max(1, Math.round(vh * scale));
    const g = grab.getContext("2d");
    g.setTransform(1, 0, 0, 1, 0, 0);
    if (el("liveMirror").checked) g.setTransform(-1, 0, 0, 1, grab.width, 0);
    g.drawImage(cam, 0, 0, grab.width, grab.height);
    const t = performance.now();
    s.sending = true;
    const id = s.id;
    grab.toBlob((blob) => {
      fetch(`/api/live/${id}/frame`, { method: "POST", headers: { "X-Capture-Time": String(t) }, body: blob })
        .catch((e) => console.error(e))
        .finally(() => { if (s?.id === id) s.sending = false; });
    }, "image/jpeg", 0.85);
  }

  // Draw the moment `now - delay`, interpolating between the keyframes around it.
  function frame() {
    if (!s) return;
    s.raf = requestAnimationFrame(frame);
    el("liveCam").hidden = !el("liveShowCam").checked;
    el("liveCam").style.transform = el("liveMirror").checked ? "scaleX(-1)" : "";
    const { meta, kfs } = s;
    if (!meta || !kfs.length) return;

    const canvas = el("liveCanvas"), box = el("liveBox");
    const scale = Math.min(box.clientWidth / meta.width, box.clientHeight / meta.height);
    const cssW = Math.max(1, Math.floor(meta.width * scale)), cssH = Math.max(1, Math.floor(meta.height * scale));
    const dpr = window.devicePixelRatio || 1;
    const w = Math.round(cssW * dpr), h = Math.round(cssH * dpr);
    if (canvas.width !== w || canvas.height !== h) {
      canvas.width = w;
      canvas.height = h;
      canvas.style.width = cssW + "px";
      canvas.style.height = cssH + "px";
    }

    const shown = performance.now() - (s.latency + 1.2 * s.interval);
    let k = 0;
    while (k + 1 < kfs.length && kfs[k + 1].t <= shown) k++;
    let u = 0;
    if (k + 1 < kfs.length && shown > kfs[k].t) u = Math.min(1, (shown - kfs[k].t) / (kfs[k + 1].t - kfs[k].t));
    const from = Math.max(0, k - 1), to = Math.min(kfs.length, k + 3);
    const window_ = { ...meta, kfps: 1, keyframes: kfs.slice(from, to).map((f) => f.shapes) };
    Motion.render(canvas.getContext("2d", { alpha: false }), window_, k - from + u, el("liveInterp").value, w, h);
  }

  function showStats() {
    const st = s.stats;
    el("liveStats").innerHTML =
      `<b>${st.kfps}</b> keyframes/s · ${st.avgMs} ms cada uno · <b>${st.shapes}</b> formas<br>` +
      `adaptadas ${st.adapted} · solo color ${st.recolored} · reemplazadas ${st.replaced} · nuevas ${st.added}` +
      (st.flowMs ? ` · flujo ${st.flowMs} ms` : "") + "<br>" +
      `retraso en pantalla ~${Math.round(s.latency + 1.2 * s.interval)} ms`;
    el("stats").innerHTML = `<b>En vivo</b> · ${st.kfps} keyframes/s · ${st.shapes} formas`;
  }

  // Record what is on the canvas, in real time.
  function toggleRecording() {
    if (!s) return;
    if (s.rec?.state === "recording") {
      s.rec.stop();
      return;
    }
    const mime = ["video/mp4;codecs=avc1", "video/mp4", "video/webm;codecs=vp9", "video/webm"]
      .find((m) => window.MediaRecorder?.isTypeSupported(m));
    if (!mime) {
      alert("Este navegador no puede grabar el canvas.");
      return;
    }
    const rec = new MediaRecorder(el("liveCanvas").captureStream(30), { mimeType: mime, videoBitsPerSecond: 8e6 });
    const chunks = [];
    rec.ondataavailable = (e) => { if (e.data.size) chunks.push(e.data); };
    rec.onstop = () => {
      el("liveRec").textContent = "● Grabar";
      const type = mime.split(";")[0];
      download(new Blob(chunks, { type }), "primitive-en-vivo." + (type === "video/mp4" ? "mp4" : "webm"));
    };
    rec.start(1000);
    s.rec = rec;
    s.recStart = performance.now();
    const tick = () => {
      if (rec.state !== "recording") return;
      el("liveRec").textContent = `■ Detener (${fmtTime((performance.now() - s.recStart) / 1000)})`;
      setTimeout(tick, 500);
    };
    tick();
  }

  el("liveRec").addEventListener("click", toggleRecording);
  for (const id of ["liveBudget", "liveAdapt", "liveFlow"]) {
    el(id).addEventListener("input", () => {
      el("liveBudgetOut").textContent = el("liveBudget").value + " ms";
      el("liveAdaptOut").textContent = el("liveAdapt").value;
      if (s) setParams(s.params);
    });
  }
  el("liveBudgetOut").textContent = el("liveBudget").value + " ms";
  el("liveAdaptOut").textContent = el("liveAdapt").value;

  return { start, stop, setParams, active: () => !!s };
})();
