import { expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";

import { RunReportSchema } from "../api/gen/mimir/v1/training_pb";
import { verdict } from "./verdict";

const report = (accuracy: number, classes: [string, number, number][]) => create(RunReportSchema, {
  kind: "class", examples: 100n, accuracy,
  classes: classes.map(([label, support, recall]) => ({ label, support: BigInt(support), recall, precision: 0.5, f1: 0.5 })),
});

test("compares with always guessing the common answer", () => {
  const good = verdict(report(0.91, [["no", 77, 0.95], ["yes", 23, 0.78]]))!;
  expect(good.level).toBe("good");
  expect(good.body).toContain("Always guessing no would get 77 of 100 right");
  const none = verdict(report(0.77, [["no", 77, 1], ["yes", 23, 0]]))!;
  expect(none.level).toBe("none");
  expect(none.next).toContain("More rows that say yes");
  const weak = verdict(report(0.83, [["no", 77, 0.95], ["yes", 23, 0.4]]))!;
  expect(weak.level).toBe("weak");
  const perfect = verdict(report(1, [["no", 77, 1], ["yes", 23, 1]]))!;
  expect(perfect.level).toBe("weak");
  expect(perfect.next).toContain("gives the answer away");
});

test("uses the server's baseline when there are too many answers to list", () => {
  const many = (accuracy: number) => create(RunReportSchema, { kind: "class", examples: 500n, accuracy, baseline: 0.4, baselineLabel: "shoes" });
  const good = verdict(many(0.8))!;
  expect(good.level).toBe("good");
  expect(good.body).toContain("Always guessing shoes would get 40 of 100 right");
  expect(verdict(many(0.41))!.level).toBe("none");
});

test("reads a number model's fit", () => {
  const r = create(RunReportSchema, { kind: "number", examples: 50n, mae: 12.4, r2: 0.62, within10: 0.7 });
  const v = verdict(r)!;
  expect(v.level).toBe("good");
  expect(v.title).toBe("It is usually off by about 12.4");
  expect(verdict(create(RunReportSchema, { kind: "number", examples: 50n, mae: 30, r2: 0.01 }))!.level).toBe("none");
});

test("reads a box-finding model", () => {
  const r = (ap50: number) => create(RunReportSchema, { kind: "boxes", examples: 40n, ap50, classes: [{ label: "cat", support: 30n, recall: 0.9 }, { label: "dog", support: 12n, recall: 0.4 }] });
  const good = verdict(r(0.7))!;
  expect(good.level).toBe("good");
  expect(good.next).toContain("It misses dog most");
  expect(verdict(r(0.3))!.level).toBe("weak");
  expect(verdict(r(0.1))!.level).toBe("none");
});

test("compares a fine-tune with before it", () => {
  const r = (logLoss: number) => create(RunReportSchema, { kind: "text", examples: 8n, accuracy: 0.6, logLoss, beforeAccuracy: 0.3, beforeLoss: 2.5 });
  const better = verdict(r(1.2))!;
  expect(better.level).toBe("good");
  expect(better.body).toContain("up from 30 before");
  expect(verdict(r(2.5))!.level).toBe("none");
  expect(verdict(create(RunReportSchema, { kind: "text", examples: 8n, accuracy: 0.6, logLoss: 1 }))).toBeUndefined();
});
