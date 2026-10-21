// Browser-only demo (GitHub Pages): the server runs in a Web Worker as
// WebAssembly, and this file routes the app's fetch and EventSource calls to
// /api/... there instead of the network. Video needs ffmpeg on the server, so
// the demo covers images and the live camera.
(() => {
  const REPO = "https://github.com/GuidoDSalem/To-Primitive";
  const worker = new Worker("worker.js");
  const pending = new Map();
  let nextID = 1;

  worker.onmessage = ({ data: { id, kind, value } }) => {
    if (kind === "fatal") {
      document.getElementById("stats").textContent = "No se pudo cargar el motor: " + value;
      return;
    }
    const h = pending.get(id);
    if (!h) return;
    h[kind]?.(value);
    if (kind === "end") pending.delete(id);
  };

  const apiPath = (url) => {
    const u = new URL(url, location.href);
    return u.origin === location.origin && u.pathname.startsWith("/api/") ? u.pathname + u.search : null;
  };
  const abort = (id) => {
    pending.delete(id);
    worker.postMessage({ type: "abort", id });
  };

  // send forwards one request to the worker and returns its id.
  async function send(path, init, handlers) {
    const req = new Request(new URL(path, location.href), init); // encodes FormData, Blob…
    const body = new Uint8Array(await req.arrayBuffer());
    const headers = {};
    req.headers.forEach((v, k) => (headers[k] = v));
    const id = nextID++;
    pending.set(id, handlers);
    worker.postMessage({ type: "req", id, method: req.method, url: path, headers, body }, [body.buffer]);
    return id;
  }

  const unsupported = (path, method) =>
    path.startsWith("/api/videos") && method === "POST" &&
    new Response("la demo en el navegador no procesa video (necesita ffmpeg en el servidor). Probá con una imagen o la cámara.", { status: 501 });

  const realFetch = window.fetch.bind(window);
  window.fetch = (input, init = {}) => {
    const path = apiPath(typeof input === "string" ? input : input.url);
    if (!path) return realFetch(input, init);
    const blocked = unsupported(path, (init.method || "GET").toUpperCase());
    if (blocked) return Promise.resolve(blocked);
    return new Promise((resolve, reject) => {
      let ctrl;
      const stream = new ReadableStream({ start: (c) => (ctrl = c) });
      send(path, init, {
        head: ({ status, headers }) => {
          const empty = [101, 204, 205, 304].includes(status);
          resolve(new Response(empty ? null : stream, { status, headers }));
        },
        chunk: (b) => ctrl.enqueue(b),
        end: () => ctrl.close(),
      }).then((id) => init.signal?.addEventListener("abort", () => abort(id)), reject);
    });
  };

  // EventSource over the worker, with the same reconnect-on-drop behaviour.
  const RealEventSource = window.EventSource;
  window.EventSource = class extends EventTarget {
    constructor(url) {
      super();
      const path = apiPath(url);
      if (!path) return new RealEventSource(url);
      this.url = url;
      this.path = path;
      this.readyState = 0;
      this.onopen = this.onmessage = this.onerror = null;
      this.open();
    }
    open() {
      const dec = new TextDecoder();
      let text = "";
      send(this.path, {}, {
        head: ({ status }) => {
          if (status !== 200) return this.fail(false);
          this.readyState = 1;
          this.fire(new Event("open"), "onopen");
        },
        chunk: (b) => {
          text += dec.decode(b, { stream: true });
          for (let i; (i = text.indexOf("\n\n")) >= 0; text = text.slice(i + 2)) this.dispatch(text.slice(0, i));
        },
        end: () => this.readyState !== 2 && this.fail(true),
      }).then((id) => (this.readyState === 2 ? abort(id) : (this.id = id)));
    }
    dispatch(block) {
      let event = "message";
      const data = [];
      for (const line of block.split("\n")) {
        if (!line || line.startsWith(":")) continue;
        const k = line.indexOf(":");
        const field = k < 0 ? line : line.slice(0, k);
        const value = k < 0 ? "" : line.slice(k + 1).replace(/^ /, "");
        if (field === "event") event = value;
        else if (field === "data") data.push(value);
      }
      if (data.length) this.fire(new MessageEvent(event, { data: data.join("\n") }), event === "message" ? "onmessage" : null);
    }
    fire(e, prop) {
      this.dispatchEvent(e);
      if (prop) this[prop]?.(e);
    }
    fail(retry) {
      this.readyState = retry ? 0 : 2;
      this.fire(new Event("error"), "onerror");
      if (retry) setTimeout(() => this.readyState === 0 && this.open(), 1000);
    }
    close() {
      this.readyState = 2;
      if (this.id) abort(this.id);
    }
  };

  // Images only, and a note on what the full version adds.
  const file = document.getElementById("file");
  file.accept = "image/*";
  file.parentElement.lastChild.textContent = "Subir imagen…";
  const note = document.createElement("p");
  note.className = "hint demo-note";
  note.innerHTML = `Demo en el navegador: imágenes y cámara en vivo, calculadas en tu dispositivo. ` +
    `El video y la exportación de reels necesitan el servidor: <a href="${REPO}" target="_blank" rel="noopener">ver en GitHub</a>.`;
  document.querySelector("#panel header").append(note);
})();
