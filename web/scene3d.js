"use strict";

// 3D scene of an image's shapes: every shape is a flat piece floating at its
// own depth, scaled up in proportion to its distance ("anamorphic"), so that
// from the front camera all of them line up exactly into the image, and any
// other viewpoint shows them as layers with parallax. Shapes can fly in from
// (or out to) the depth, explode, or fall, while the camera moves.
//
// Pure Canvas 2D: shapes are turned into polygons (ellipses and curves are
// sampled) and their vertices projected with a perspective camera, which is
// exact for flat polygons. From the front they are drawn in primitive's
// order, so colours and transparency blend exactly as in the image.
const Scene3D = (() => {
  const H = 6;
  const FOV = 40 * Math.PI / 180;
  const clamp01 = (x) => Math.min(1, Math.max(0, x));
  const smooth = (p) => (p < 0.5 ? 4 * p ** 3 : 1 - (-2 * p + 2) ** 3 / 2);

  const DEPTHS = {
    order: "Por orden (las primeras al fondo)",
    size: "Por tamaño (grandes al fondo)",
    light: "Por brillo (claras adelante)",
    random: "Aleatoria",
  };
  const ASSEMBLIES = {
    depth: "Llegan desde el fondo",
    explode: "Explosión (se arman desde todas partes)",
    fall: "Caen desde arriba",
    none: "Sin movimiento (aparecen con fundido)",
  };
  const CAMERAS = {
    reveal: "Revelar: termina de frente al completarse",
    sway: "Vaivén",
    sweep: "Barrido de lado a lado",
    orbit: "Vuelta completa",
    static: "Fija, de frente",
  };

  // Outline of a shape in image coordinates (with primitive's half-pixel
  // offset). Returns { pts: [x0, y0, x1, y1, ...], closed, width }.
  function outline(s) {
    const pts = [];
    switch (s[0]) {
      case 0:
        for (let j = H; j < s.length; j += 2) pts.push(s[j] + 0.5, s[j + 1] + 0.5);
        return { pts, closed: true };
      case 1: {
        const [cx, cy, w, h, a] = s.slice(H, H + 5);
        const c = Math.cos(a * Math.PI / 180), n = Math.sin(a * Math.PI / 180);
        for (const [dx, dy] of [[-w / 2, -h / 2], [w / 2, -h / 2], [w / 2, h / 2], [-w / 2, h / 2]]) {
          pts.push(cx + 0.5 + dx * c - dy * n, cy + 0.5 + dx * n + dy * c);
        }
        return { pts, closed: true };
      }
      case 2: {
        const [cx, cy, rx, ry, a] = s.slice(H, H + 5);
        const c = Math.cos(a * Math.PI / 180), n = Math.sin(a * Math.PI / 180);
        const k = 48;
        for (let j = 0; j < k; j++) {
          const t = (j / k) * 2 * Math.PI, ex = rx * Math.cos(t), ey = ry * Math.sin(t);
          pts.push(cx + 0.5 + ex * c - ey * n, cy + 0.5 + ex * n + ey * c);
        }
        return { pts, closed: true };
      }
      case 3: {
        const [x1, y1, qx, qy, x2, y2, width] = s.slice(H, H + 7);
        const k = 20;
        for (let j = 0; j <= k; j++) {
          const t = j / k, u = 1 - t;
          pts.push(u * u * x1 + 2 * u * t * qx + t * t * x2 + 0.5, u * u * y1 + 2 * u * t * qy + t * t * y2 + 0.5);
        }
        return { pts, closed: false, width };
      }
    }
    return { pts, closed: true };
  }

  // Rodrigues rotation of v around unit axis k by angle a.
  function rotate(v, k, a) {
    const c = Math.cos(a), s = Math.sin(a);
    const d = (k[0] * v[0] + k[1] * v[1] + k[2] * v[2]) * (1 - c);
    return [
      v[0] * c + (k[1] * v[2] - k[2] * v[1]) * s + k[0] * d,
      v[1] * c + (k[2] * v[0] - k[0] * v[2]) * s + k[1] * d,
      v[2] * c + (k[0] * v[1] - k[1] * v[0]) * s + k[2] * d,
    ];
  }

  // opts: the recording options (curve, order, direction, duration, holds)
  // plus { depthMode, depth, assembly, scatter, camera, angle, stagger }.
  function plan(img, opts) {
    const shapes = img.shapes, n = shapes.length;
    const W = img.width, Hh = img.height;
    const D = (Hh / 2) / Math.tan(FOV / 2); // camera distance that frames the image
    const stagger = clamp01(opts.stagger);
    const flight = Math.max(0.05, opts.duration * (1 - 0.9 * stagger));
    const sched = Build.schedule(img, opts, flight);

    // Depth of each shape, 0 (front) .. 1 (back).
    const depthKey = {
      order: (i) => 1 - i / Math.max(1, n - 1),
      size: (i) => sched.geometry[i].area,
      light: (i) => -(0.299 * shapes[i][1] + 0.587 * shapes[i][2] + 0.114 * shapes[i][3]),
      random: null,
    }[opts.depthMode];
    const norm = new Float64Array(n);
    if (opts.depthMode === "random") {
      Build.shuffled(n).forEach((i, r) => { norm[i] = n > 1 ? r / (n - 1) : 0.5; });
    } else if (opts.depthMode === "order") {
      for (let i = 0; i < n; i++) norm[i] = depthKey(i);
    } else {
      // Rank-based so a few outliers don't squash everyone else together.
      [...Array(n).keys()].sort((a, b) => depthKey(a) - depthKey(b)).forEach((i, r) => { norm[i] = n > 1 ? r / (n - 1) : 0.5; });
    }

    // Per-shape geometry in the world: anamorphic placement, plus where it
    // comes from while assembling.
    let seed = 987654321;
    const rnd = () => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff);
    const pieces = shapes.map((s, i) => {
      const { pts, closed, width } = outline(s);
      const z = Math.max(-0.75 * D, opts.depth * Hh * (norm[i] - 0.5));
      const k = (D + z) / D;
      const world = [];
      let cx = 0, cy = 0;
      for (let j = 0; j < pts.length; j += 2) {
        const p = [(pts[j] - W / 2) * k, (pts[j + 1] - Hh / 2) * k, z];
        world.push(p);
        cx += p[0]; cy += p[1];
      }
      const center = [cx / world.length, cy / world.length, z];
      // Where the shape starts (offset from its place) and how it is turned.
      const sc = opts.scatter;
      let off = [0, 0, 0], axis = [0, 0, 1], spin = 0;
      if (opts.assembly === "depth") off = [0, 0, (2 + 3 * rnd()) * Hh * sc];
      else if (opts.assembly === "fall") off = [(rnd() - 0.5) * W * 0.3 * sc, -(1.5 + rnd()) * Hh * sc, (rnd() - 0.5) * Hh * sc];
      else if (opts.assembly === "explode") {
        const th = rnd() * 2 * Math.PI, ph = Math.acos(2 * rnd() - 1), r = (0.8 + rnd()) * Hh * 1.2 * sc;
        off = [r * Math.sin(ph) * Math.cos(th), r * Math.sin(ph) * Math.sin(th), r * Math.cos(ph)];
      }
      if (opts.assembly === "explode" || opts.assembly === "fall") {
        const th = rnd() * 2 * Math.PI, ph = Math.acos(2 * rnd() - 1);
        axis = [Math.sin(ph) * Math.cos(th), Math.sin(ph) * Math.sin(th), Math.cos(ph)];
        spin = (1 + 2 * rnd()) * Math.PI * sc;
      }
      return { s, world, center, closed, width: width ? width * k : 0, off, axis, spin };
    });

    function camera(t, bt) {
      const A = opts.angle * Math.PI / 180;
      const u = sched.total > 0 ? t / sched.total : 0;
      switch (opts.camera) {
        case "reveal": { const r = 1 - smooth(clamp01(bt / sched.main)); return [A * r, A * 0.35 * r]; }
        case "sway": return [A * Math.sin(2 * Math.PI * u), A * 0.25 * Math.sin(4 * Math.PI * u)];
        case "sweep": return [A * (2 * u - 1), A * 0.15 * Math.sin(Math.PI * u)];
        case "orbit": return [2 * Math.PI * u, A * 0.2 * Math.sin(2 * Math.PI * u)];
        default: return [0, 0];
      }
    }

    function render(ctx, t, w, h) {
      ctx.setTransform(1, 0, 0, 1, 0, 0);
      ctx.fillStyle = img.background;
      ctx.fillRect(0, 0, w, h);
      const bt = sched.buildTime(t);
      const [yaw, pitch] = camera(t, bt);

      // Camera on a sphere of radius D around the image center, looking at it.
      const cp = Math.cos(pitch);
      const P = [-D * cp * Math.sin(yaw), -D * Math.sin(pitch), -D * cp * Math.cos(yaw)];
      const fwd = [-P[0] / D, -P[1] / D, -P[2] / D];
      const up = [0, -1, 0];
      let right = [fwd[1] * up[2] - fwd[2] * up[1], fwd[2] * up[0] - fwd[0] * up[2], fwd[0] * up[1] - fwd[1] * up[0]];
      const rl = Math.hypot(...right) || 1;
      right = right.map((v) => v / rl);
      const down = [fwd[1] * right[2] - fwd[2] * right[1], fwd[2] * right[0] - fwd[0] * right[2], fwd[0] * right[1] - fwd[1] * right[0]];
      const F = (h / 2) / Math.tan(FOV / 2);
      const near = 0.05 * D;

      const proj = (p) => {
        const v = [p[0] - P[0], p[1] - P[1], p[2] - P[2]];
        const zc = v[0] * fwd[0] + v[1] * fwd[1] + v[2] * fwd[2];
        if (zc < near) return null;
        return [w / 2 + (F * (v[0] * right[0] + v[1] * right[1] + v[2] * right[2])) / zc,
          h / 2 + (F * (v[0] * down[0] + v[1] * down[1] + v[2] * down[2])) / zc, zc];
      };

      // Drawn in primitive's order so the front view blends exactly like the
      // image; seen from behind, the order is reversed so nearer shapes
      // still cover farther ones.
      const behind = fwd[2] < 0;
      for (let o = 0; o < n; o++) {
        const i = behind ? n - 1 - o : o;
        const pc = pieces[i];
        const local = clamp01((bt - sched.startOf(i)) / flight);
        if (local <= 0) continue;
        const a = stagger < 0.05 ? sched.ease(local) : smooth(local); // 1 = in place
        const rest = 1 - a;
        // Fade in while arriving (and only fade for "none").
        let factor = opts.assembly === "none" ? a : Math.min(1, local * 4);
        if (factor <= 0) continue;

        const c = pc.center;
        const angle = pc.spin * rest;
        const screen = [];
        let ok = true, zsum = 0;
        for (const p of pc.world) {
          let q = p;
          if (angle) {
            const r = rotate([p[0] - c[0], p[1] - c[1], p[2] - c[2]], pc.axis, angle);
            q = [c[0] + r[0], c[1] + r[1], c[2] + r[2]];
          }
          q = [q[0] + pc.off[0] * rest, q[1] + pc.off[1] * rest, q[2] + pc.off[2] * rest];
          const s = proj(q);
          if (!s) { ok = false; break; }
          screen.push(s);
          zsum += s[2];
        }
        if (!ok || screen.length < 2) continue;

        const s = pc.s;
        const color = `rgba(${s[1] | 0},${s[2] | 0},${s[3] | 0},${(s[4] / 255) * factor})`;
        ctx.beginPath();
        ctx.moveTo(screen[0][0], screen[0][1]);
        for (let j = 1; j < screen.length; j++) ctx.lineTo(screen[j][0], screen[j][1]);
        if (pc.closed) {
          ctx.closePath();
          ctx.fillStyle = color;
          ctx.fill();
        } else {
          ctx.lineWidth = (pc.width * F) / (zsum / screen.length);
          ctx.lineCap = "round";
          ctx.lineJoin = "round";
          ctx.strokeStyle = color;
          ctx.stroke();
        }
      }
    }

    return { width: W, height: Hh, duration: sched.total, render, ease: sched.ease };
  }

  return { DEPTHS, ASSEMBLIES, CAMERAS, plan };
})();
