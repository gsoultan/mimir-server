import { expect, test } from "bun:test";

import { examplesWanted, layersOf, learned, medium, sizes } from "./shape";

test("learned counts weights and biases, skipping dropout", () => {
  // 7 -> 64 -> 32 -> 2: the design screen's example.
  const steps = [{ kind: "dense", units: 64, rate: 0 }, { kind: "dropout", units: 0, rate: 0.1 }, { kind: "dense", units: 32, rate: 0 }] as const;
  expect(learned(7, [...steps], 2)).toBe(7 * 64 + 64 + 64 * 32 + 32 + 32 * 2 + 2);
});

test("medium matches the server's usual shape", () => {
  expect(medium(3)).toEqual([{ kind: "dense", units: 16, rate: 0 }, { kind: "dense", units: 16, rate: 0 }]);
  expect(medium(100)[0]!.units).toBe(256);
  expect(layersOf("medium", [], 7)).toEqual([]);
  expect(layersOf("small", [], 7)).toEqual(sizes.small(7));
});

test("examplesWanted rounds to friendly figures", () => {
  expect(examplesWanted(10)).toBe(200);
  expect(examplesWanted(2689)).toBe(1500);
  expect(examplesWanted(100_001)).toBe(55_000);
});
