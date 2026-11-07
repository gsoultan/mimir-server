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

test("reads a number model's fit", () => {
  const r = create(RunReportSchema, { kind: "number", examples: 50n, mae: 12.4, r2: 0.62, within10: 0.7 });
  const v = verdict(r)!;
  expect(v.level).toBe("good");
  expect(v.title).toBe("It is usually off by about 12.4");
  expect(verdict(create(RunReportSchema, { kind: "number", examples: 50n, mae: 30, r2: 0.01 }))!.level).toBe("none");
});
