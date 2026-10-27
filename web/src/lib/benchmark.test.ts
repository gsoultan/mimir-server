import { expect, test } from "bun:test";

import { create } from "@bufbuild/protobuf";

import { BenchmarkSchema, BenchmarkScoreSchema } from "../api/gen/mimir/v1/benchmark_pb";
import { behind, headlineText, medal, metricHint, metricTitle } from "./benchmark";

const labels = create(BenchmarkSchema, { kind: "class", metric: "right answers" });
const balanced = create(BenchmarkSchema, { kind: "class", metric: "balanced score (macro F1)" });
const numbers = create(BenchmarkSchema, { kind: "number", metric: "average miss (MAE)", lowerIsBetter: true });

test("headlineText reads a score the way its test measures it", () => {
  expect(headlineText(labels, 0.9123)).toBe("91.2%");
  expect(headlineText(balanced, 0.9123)).toBe("0.912");
  expect(headlineText(numbers, 3.456)).toBe("off by 3.46");
});

test("metricHint and metricTitle say what the leaderboard sorts by", () => {
  expect(metricHint(numbers)).toContain("less is better");
  expect(metricHint(balanced)).toContain("rare");
  expect(metricTitle(labels)).toBe("Right answers");
  expect(metricTitle(create(BenchmarkSchema, {}))).toBe("Score");
});

test("medal marks the first three places only", () => {
  expect([medal(1), medal(2), medal(3), medal(4), medal(0)]).toEqual(["🥇", "🥈", "🥉", "", ""]);
});

test("behind measures the gap to the leader, both ways round", () => {
  const top = create(BenchmarkScoreSchema, { rank: 1, headline: 0.9 });
  const next = create(BenchmarkScoreSchema, { rank: 2, headline: 0.82 });
  const unscored = create(BenchmarkScoreSchema, { rank: 0 });
  expect(behind(labels, [top, next], top)).toBe("the best");
  expect(behind(labels, [top, next], next)).toBe("8.0 points behind");
  expect(behind(balanced, [top, next], next)).toBe("0.08 behind");
  expect(behind(labels, [top, next], unscored)).toBe("");
  const tied = create(BenchmarkScoreSchema, { rank: 2, headline: 0.9 });
  expect(behind(labels, [top, tied], tied)).toBe("the same score");

  const best = create(BenchmarkScoreSchema, { rank: 1, headline: 2 });
  const worse = create(BenchmarkScoreSchema, { rank: 2, headline: 5.5 });
  expect(behind(numbers, [best, worse], worse)).toBe("3.5 further off");
});
