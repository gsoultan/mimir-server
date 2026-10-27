import { expect, test } from "bun:test";

import { create } from "@bufbuild/protobuf";

import { ClassScoreSchema, RunReportSchema } from "../api/gen/mimir/v1/training_pb";
import { balanced, headline } from "./report";

test("headline speaks for each kind of model", () => {
  expect(headline(create(RunReportSchema, { kind: "class", accuracy: 0.917 }))).toBe("gets 92 of 100 right");
  expect(headline(create(RunReportSchema, { kind: "number", mae: 12.345 }))).toBe("usually off by about 12.35");
  expect(headline(create(RunReportSchema, { kind: "number", mae: 15737.1 }))).toBe("usually off by about 15,737");
  expect(headline(create(RunReportSchema, { kind: "ranked", k: 5, hitAt: [0.3, 0.64] }))).toBe("the right one is in its top 5 for 64 of 100");
});

test("balanced spots a rare answer", () => {
  const even = create(RunReportSchema, { classes: [create(ClassScoreSchema, { support: 50n }), create(ClassScoreSchema, { support: 50n })] });
  const rare = create(RunReportSchema, { classes: [create(ClassScoreSchema, { support: 95n }), create(ClassScoreSchema, { support: 5n })] });
  expect(balanced(even)).toBe(false);
  expect(balanced(rare)).toBe(true);
});
