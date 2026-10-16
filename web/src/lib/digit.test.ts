import { expect, test } from "bun:test";
import { size, toMNIST } from "./digit";

function canvas(w: number, h: number, ink: (x: number, y: number) => boolean): Uint8ClampedArray {
  const px = new Uint8ClampedArray(w * h * 4);
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const v = ink(x, y) ? 0 : 255;
      px.set([v, v, v, 255], (y * w + x) * 4);
    }
  }
  return px;
}

test("blank page is all zeros", () => {
  const out = toMNIST(canvas(100, 100, () => false), 100, 100);
  expect(out.length).toBe(size * size);
  expect(out.every((v) => v === 0)).toBe(true);
});

test("a stroke in the corner ends up centred", () => {
  const out = toMNIST(canvas(200, 200, (x, y) => x > 10 && x < 30 && y > 10 && y < 90), 200, 200);
  let mass = 0, cx = 0, cy = 0;
  out.forEach((v, i) => { mass += v; cx += v * (i % size); cy += v * Math.floor(i / size); });
  expect(mass).toBeGreaterThan(0);
  expect(Math.abs(cx / mass - size / 2)).toBeLessThan(1.5);
  expect(Math.abs(cy / mass - size / 2)).toBeLessThan(1.5);
  expect(Math.max(...out)).toBeLessThanOrEqual(1);
});
