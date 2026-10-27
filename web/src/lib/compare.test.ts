import { expect, test } from "bun:test";

import { create } from "@bufbuild/protobuf";

import { EpochResultSchema, JobSchema, LayerSchema } from "../api/gen/mimir/v1/training_pb";
import { againName, bestPass, parseIds, shapeWord, took, trainingSeconds } from "./compare";

test("parseIds drops blanks and repeats, keeping the order", () => {
  expect(parseIds("b, a,,b")).toEqual(["b", "a"]);
  expect(parseIds(undefined)).toEqual([]);
});

test("trainingSeconds and bestPass read every pass", () => {
  const j = create(JobSchema, {
    history: [
      create(EpochResultSchema, { epoch: 1, accuracy: 0.6, durationMillis: 1500n }),
      create(EpochResultSchema, { epoch: 2, accuracy: 0.9, durationMillis: 2500n }),
      create(EpochResultSchema, { epoch: 3, accuracy: 0.8, durationMillis: 1000n }),
    ],
  });
  expect(trainingSeconds(j)).toBe(5);
  expect(bestPass(j)).toEqual({ epoch: 2, accuracy: 0.9 });
  expect(bestPass(create(JobSchema, {}))).toBeUndefined();
});

test("took is brief", () => {
  expect(took(0.02)).toBe("under 0.1 s");
  expect(took(4.25)).toBe("4.3 s");
  expect(took(42)).toBe("42 s");
  expect(took(185)).toBe("3 min 5 s");
  expect(took(120)).toBe("2 min");
  expect(took(4800)).toBe("1 h 20 min");
});

test("shapeWord and againName", () => {
  expect(shapeWord([])).toBe("Usual");
  expect(shapeWord([create(LayerSchema, { kind: "dense", units: 32 }), create(LayerSchema, { kind: "dropout", rate: 0.2 })])).toBe("32 units → forget 20%");
  expect(againName("Churn")).toBe("Churn (again)");
  expect(againName("Churn (again)")).toBe("Churn (again 2)");
  expect(againName("Churn (again 2)")).toBe("Churn (again 3)");
});
