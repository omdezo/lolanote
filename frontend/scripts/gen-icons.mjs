// Raster PWA icons, generated rather than committed as opaque binaries.
//
// MO10. `manifest.webmanifest` declared exactly one icon — an SVG at
// `sizes:"any"` — and `index.html` declared no `apple-touch-icon` at all. iOS
// Safari ignores manifest icons entirely and requires `apple-touch-icon`, so
// Add-to-Home-Screen produced a screenshot of the page as the app icon. And
// with no `purpose:"maskable"`, the Android launcher drew our square on a white
// plinth instead of adopting the device's icon shape.
//
// This script rasterises the same mark `public/icon.svg` draws — the indigo
// gradient tile with a serif Q — at the sizes the two platforms require. It is
// checked in and re-runnable (`node scripts/gen-icons.mjs`) so the icons are
// reproducible from source rather than being binaries nobody can regenerate.
//
// No dependencies on purpose: a build plugin for four static files would be a
// larger commitment than the files.
import { deflateSync } from 'node:zlib';
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const OUT = resolve(dirname(fileURLToPath(import.meta.url)), '../public');

/** 4x supersampling: the mark has a big rounded corner and a circular bowl,
 *  and aliasing on either reads as a cheap icon at launcher size. */
const SS = 4;

const lerp = (a, b, t) => a + (b - a) * t;

/** The gradient of icon.svg: #6e6cf0 → #4a48c4 along the diagonal. */
function tile(u, v) {
  const t = Math.min(1, Math.max(0, (u + v) / 2));
  return [lerp(0x6e, 0x4a, t), lerp(0x6c, 0x48, t), lerp(0xf0, 0xc4, t)];
}

/** Signed "inside" test for a rounded rectangle in unit space. */
function insideRounded(x, y, size, radius) {
  const rx = Math.max(radius - x, 0, x - (size - radius));
  const ry = Math.max(radius - y, 0, y - (size - radius));
  if (x < 0 || y < 0 || x > size || y > size) return false;
  return rx * rx + ry * ry <= radius * radius;
}

/**
 * The Q, as geometry rather than as a font.
 *
 * A serif Q at icon size is a ring plus a tail; drawing it directly keeps the
 * icon identical on a build machine with no fonts installed, which is the
 * failure mode a text-based rasteriser has and nobody notices until the CDN
 * serves a blank tile.
 */
function inQ(x, y, size) {
  const cx = size * 0.5, cy = size * 0.47;
  const rOuter = size * 0.255, rInner = size * 0.15;
  const d = Math.hypot(x - cx, y - cy);
  if (d <= rOuter && d >= rInner) return true;
  // The tail: a thick stroke from inside the lower-right of the bowl outward.
  const tx = x - (cx + size * 0.11), ty = y - (cy + size * 0.16);
  const along = (tx + ty) / Math.SQRT2;
  const across = (ty - tx) / Math.SQRT2;
  return along >= -size * 0.06 && along <= size * 0.15 && Math.abs(across) <= size * 0.043;
}

function render(size, { maskable }) {
  // A maskable icon must keep everything meaningful inside the safe zone —
  // the middle 80% — because the launcher may crop to a circle.
  const inset = maskable ? size * 0.1 : 0;
  const art = size - inset * 2;
  const radius = maskable ? art : art * 0.199; // full-bleed circle vs the SVG's rx=102/512
  const px = Buffer.alloc(size * size * 4);

  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      let r = 0, g = 0, b = 0, a = 0;
      for (let sy = 0; sy < SS; sy++) {
        for (let sx = 0; sx < SS; sx++) {
          const fx = x + (sx + 0.5) / SS - inset;
          const fy = y + (sy + 0.5) / SS - inset;
          // Maskable fills the whole square with the gradient (the launcher
          // does the shaping); the plain icon draws its own rounded tile.
          const on = maskable
            ? fx >= -inset && fy >= -inset && fx <= art + inset && fy <= art + inset
            : insideRounded(fx, fy, art, radius);
          if (!on) continue;
          const [tr, tg, tb] = tile(fx / art, fy / art);
          if (inQ(fx, fy, art)) { r += 255; g += 255; b += 255; } else { r += tr; g += tg; b += tb; }
          a += 255;
        }
      }
      const n = SS * SS;
      const i = (y * size + x) * 4;
      const cover = a / n / 255;
      px[i] = cover ? Math.round(r / (a / 255)) : 0;
      px[i + 1] = cover ? Math.round(g / (a / 255)) : 0;
      px[i + 2] = cover ? Math.round(b / (a / 255)) : 0;
      px[i + 3] = Math.round(a / n);
    }
  }
  return px;
}

function crc32(buf) {
  let c = ~0;
  for (const byte of buf) {
    c ^= byte;
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
  }
  return ~c >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

function png(size, px) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8;   // bit depth
  ihdr[9] = 6;   // RGBA
  // Filter type 0 on every scanline: these are tiny and the gradient deflates
  // well enough that adaptive filtering would be effort with no reader.
  const raw = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y++) {
    raw[y * (size * 4 + 1)] = 0;
    px.copy(raw, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

mkdirSync(OUT, { recursive: true });
const targets = [
  ['icon-192.png', 192, { maskable: false }],
  ['icon-512.png', 512, { maskable: false }],
  ['icon-maskable-512.png', 512, { maskable: true }],
  ['apple-touch-icon.png', 180, { maskable: false }],
];
for (const [name, size, opts] of targets) {
  writeFileSync(resolve(OUT, name), png(size, render(size, opts)));
  console.log('wrote', name, size);
}
