// Runs the Go server (compiled to WebAssembly) and answers the requests that
// demo.js forwards from the page. See wasm.go for the other half.
importScripts("wasm_exec.js");

let ready;
const started = new Promise((r) => (ready = r));
self.goReady = () => ready();
self.goEmit = (id, kind, value) => {
  postMessage({ id, kind, value }, kind === "chunk" ? [value.buffer] : []);
};

const go = new Go();
WebAssembly.instantiateStreaming(fetch("to-primitive.wasm"), go.importObject)
  .then((r) => go.run(r.instance))
  .catch((e) => postMessage({ id: 0, kind: "fatal", value: String(e) }));

onmessage = async ({ data: m }) => {
  await started;
  if (m.type === "req") goServe(m.id, m.method, m.url, m.headers, m.body);
  else if (m.type === "abort") goAbort(m.id);
};
