import { expect, test } from "bun:test";
import { clock, wav } from "./media";

test("wav writes a 16-bit mono PCM file", () => {
  const out = wav(new Float32Array([0, 1, -1, 0.5]), 16000);
  const v = new DataView(out.buffer);
  expect(String.fromCharCode(...out.slice(0, 4))).toBe("RIFF");
  expect(String.fromCharCode(...out.slice(8, 12))).toBe("WAVE");
  expect(v.getUint16(22, true)).toBe(1);
  expect(v.getUint32(24, true)).toBe(16000);
  expect(v.getUint32(40, true)).toBe(8);
  expect([0, 1, 2, 3].map((i) => v.getInt16(44 + 2 * i, true))).toEqual([0, 32767, -32768, 16384]);
});

test("clock writes minutes and hours", () => {
  expect(clock(0)).toBe("0:00");
  expect(clock(75.9)).toBe("1:15");
  expect(clock(3725)).toBe("1:02:05");
});
