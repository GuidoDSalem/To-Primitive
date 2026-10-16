"use strict";

// Minimal MP4 writer for one video track encoded by WebCodecs (H.264 or VP9).
// Writes a non-fragmented file with the moov box first ("fast start"), so it
// plays while downloading. No audio: the server adds it when asked.
const Mp4 = (() => {
  const enc = new TextEncoder();

  function concat(parts) {
    let n = 0;
    for (const p of parts) n += p.length;
    const out = new Uint8Array(n);
    let o = 0;
    for (const p of parts) { out.set(p, o); o += p.length; }
    return out;
  }

  const u8 = (v) => Uint8Array.of(v & 0xff);
  const u16 = (v) => Uint8Array.of((v >> 8) & 0xff, v & 0xff);
  const u32 = (v) => { const b = new Uint8Array(4); new DataView(b.buffer).setUint32(0, v >>> 0); return b; };
  const i32 = (v) => { const b = new Uint8Array(4); new DataView(b.buffer).setInt32(0, v); return b; };
  const zeros = (n) => new Uint8Array(n);
  const str = (s) => enc.encode(s);

  const box = (type, ...payload) => {
    const body = concat(payload);
    return concat([u32(8 + body.length), str(type), body]);
  };
  const fullBox = (type, version, flags, ...payload) =>
    box(type, u8(version), Uint8Array.of((flags >> 16) & 0xff, (flags >> 8) & 0xff, flags & 0xff), ...payload);

  const MATRIX = concat([0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000].map(u32));

  function sampleEntry(t) {
    const name = zeros(32);
    const label = str(t.codec === "avc" ? "To-Primitive H.264" : "To-Primitive VP9");
    name[0] = label.length;
    name.set(label, 1);
    const common = [
      zeros(6), u16(1),                 // reserved, data_reference_index
      u16(0), u16(0), zeros(12),        // pre_defined, reserved, pre_defined
      u16(t.width), u16(t.height),
      u32(0x00480000), u32(0x00480000), // 72 dpi
      u32(0), u16(1), name,             // reserved, frame_count, compressorname
      u16(0x0018), Uint8Array.of(0xff, 0xff),
    ];
    if (t.codec === "avc") {
      if (!t.description) throw new Error("H.264 encoder gave no avcC description");
      return box("avc1", ...common, box("avcC", new Uint8Array(t.description)));
    }
    // vpcC v1: profile 0, level, 8-bit 4:2:0 (colocated with luma), BT.709.
    const vpcC = fullBox("vpcC", 1, 0, u8(0), u8(t.vp9Level || 41), u8((8 << 4) | (1 << 1)),
      u8(1), u8(1), u8(1), u16(0));
    return box("vp09", ...common, vpcC);
  }

  // samples: [{ data: Uint8Array, pts: µs, key: bool }] in decode order.
  // Returns a Blob.
  function write(t) {
    const { samples, fps } = t;
    if (!samples.length) throw new Error("no frames encoded");
    const timescale = Math.round(fps * 1000);
    const delta = 1000;
    const count = samples.length;
    const mediaDuration = count * delta;
    const movieDuration = Math.round((count / fps) * 1000); // mvhd timescale is 1000

    // Decoding times are evenly spaced; composition offsets cover reordering
    // (B-frames), if the encoder produced any.
    const pts = samples.map((s) => Math.round((s.pts / 1e6) * timescale));
    const sorted = [...pts].sort((a, b) => a - b);
    const offsets = pts.map((p, i) => p - sorted[i]);
    const reordered = offsets.some((o) => o !== 0);

    const stblBoxes = (chunkOffset) => [
      fullBox("stsd", 0, 0, u32(1), sampleEntry(t)),
      fullBox("stts", 0, 0, u32(1), u32(count), u32(delta)),
      ...(reordered ? [fullBox("ctts", 1, 0, u32(count), ...offsets.flatMap((o) => [u32(1), i32(o)]))] : []),
      fullBox("stss", 0, 0, u32(samples.filter((s) => s.key).length),
        ...samples.flatMap((s, i) => (s.key ? [u32(i + 1)] : []))),
      fullBox("stsc", 0, 0, u32(1), u32(1), u32(count), u32(1)),
      fullBox("stsz", 0, 0, u32(0), u32(count), ...samples.map((s) => u32(s.data.length))),
      fullBox("stco", 0, 0, u32(1), u32(chunkOffset)),
    ];

    const moov = (chunkOffset) => box("moov",
      fullBox("mvhd", 0, 0, u32(0), u32(0), u32(1000), u32(movieDuration),
        u32(0x00010000), u16(0x0100), zeros(10), MATRIX, zeros(24), u32(2)),
      box("trak",
        fullBox("tkhd", 0, 3, u32(0), u32(0), u32(1), u32(0), u32(movieDuration), zeros(8),
          u16(0), u16(0), u16(0), u16(0), MATRIX, u32(t.width << 16), u32(t.height << 16)),
        box("mdia",
          fullBox("mdhd", 0, 0, u32(0), u32(0), u32(timescale), u32(mediaDuration), u16(0x55c4), u16(0)),
          fullBox("hdlr", 0, 0, u32(0), str("vide"), zeros(12), str("VideoHandler\0")),
          box("minf",
            fullBox("vmhd", 0, 1, u16(0), u16(0), u16(0), u16(0)),
            box("dinf", fullBox("dref", 0, 0, u32(1), fullBox("url ", 0, 1))),
            box("stbl", ...stblBoxes(chunkOffset))))));

    const ftyp = box("ftyp", str("isom"), u32(512), str("isom"), str("iso2"), str(t.codec === "avc" ? "avc1" : "vp09"), str("mp41"));
    let dataSize = 0;
    for (const s of samples) dataSize += s.data.length;
    if (8 + dataSize > 0xffffffff) throw new Error("video too large for this exporter (>4 GB)");

    // The moov size doesn't depend on the offset value, so measure it first.
    const moovSize = moov(0).length;
    const mdatHeader = concat([u32(8 + dataSize), str("mdat")]);
    return new Blob([ftyp, moov(ftyp.length + moovSize + 8), mdatHeader, ...samples.map((s) => s.data)], { type: "video/mp4" });
  }

  return { write };
})();
