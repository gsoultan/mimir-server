// toMNIST turns a drawing into the 28×28 input the digit models expect:
// white ink on black, centred by its centre of mass in a 20×20 box, values
// from 0 to 1. That's how the MNIST training images were made, and models
// guess much better when drawings match them.

export const size = 28;

// rgba is the canvas's pixels, width × height, dark ink on a light page.
export function toMNIST(rgba: Uint8ClampedArray, width: number, height: number): Float32Array {
  const ink = new Float32Array(width * height);
  let minX = width, minY = height, maxX = -1, maxY = -1;
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      const i = y * width + x;
      const p = i * 4;
      const lum = (0.299 * rgba[p]! + 0.587 * rgba[p + 1]! + 0.114 * rgba[p + 2]!) / 255;
      const v = (1 - lum) * (rgba[p + 3]! / 255);
      ink[i] = v;
      if (v > 0.1) {
        minX = Math.min(minX, x); maxX = Math.max(maxX, x);
        minY = Math.min(minY, y); maxY = Math.max(maxY, y);
      }
    }
  }
  const out = new Float32Array(size * size);
  if (maxX < 0) return out;

  // Scale the drawing's box to fit 20×20, keeping its shape.
  const box = Math.max(maxX - minX + 1, maxY - minY + 1);
  const scale = 20 / box;
  const small = new Float32Array(size * size);
  let mass = 0, cx = 0, cy = 0;
  for (let y = 0; y < 20; y++) {
    for (let x = 0; x < 20; x++) {
      // Average the source pixels that land in this cell.
      const sx0 = minX + x / scale, sy0 = minY + y / scale;
      let sum = 0, n = 0;
      for (let sy = Math.floor(sy0); sy < Math.ceil(sy0 + 1 / scale); sy++) {
        for (let sx = Math.floor(sx0); sx < Math.ceil(sx0 + 1 / scale); sx++) {
          if (sx >= 0 && sy >= 0 && sx < width && sy < height) {
            sum += ink[sy * width + sx]!;
            n++;
          }
        }
      }
      const v = n ? sum / n : 0;
      small[y * size + x] = v;
      mass += v; cx += v * x; cy += v * y;
    }
  }
  if (mass === 0) return out;

  // Shift so the centre of mass sits in the middle of the 28×28 image.
  const dx = Math.round(size / 2 - cx / mass), dy = Math.round(size / 2 - cy / mass);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const tx = x + dx, ty = y + dy;
      if (tx >= 0 && ty >= 0 && tx < size && ty < size) out[ty * size + tx] = Math.min(1, small[y * size + x]!);
    }
  }
  return out;
}
