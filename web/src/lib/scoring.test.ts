import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";

import { ArchitectureSchema } from "../api/gen/mimir/v1/model_pb";
import { ScoringSchema } from "../api/gen/mimir/v1/scoring_pb";
import { JobStatus } from "../api/gen/mimir/v1/training_pb";
import { progress, scores, statusText } from "./scoring";

describe("scores", () => {
  test("table models that answer with a label, number or list", () => {
    expect(scores(create(ArchitectureSchema, { input: "table", output: "class" }))).toBe(true);
    expect(scores(create(ArchitectureSchema, { input: "table", output: "" }))).toBe(true);
    expect(scores(create(ArchitectureSchema, { input: "table", output: "number" }))).toBe(true);
    expect(scores(create(ArchitectureSchema, { input: "table", output: "ranked" }))).toBe(true);
  });
  test("not photos, fixed shapes or writing", () => {
    expect(scores(create(ArchitectureSchema, { input: "image", output: "class" }))).toBe(false);
    expect(scores(create(ArchitectureSchema, { input: "table", output: "class", inputShape: [4n] }))).toBe(false);
    expect(scores(create(ArchitectureSchema, { input: "text", output: "text" }))).toBe(false);
    expect(scores(undefined)).toBe(false);
  });
});

describe("progress", () => {
  test("counts done rows, and is full once finished", () => {
    expect(progress(create(ScoringSchema, { status: JobStatus.RUNNING, rows: 200n, done: 50n }))).toBe(25);
    expect(progress(create(ScoringSchema, { status: JobStatus.RUNNING }))).toBe(0);
    expect(progress(create(ScoringSchema, { status: JobStatus.SUCCEEDED }))).toBe(100);
  });
  test("says what happened", () => {
    expect(statusText(create(ScoringSchema, { status: JobStatus.SUCCEEDED, done: 1200n, failed: 3n }))).toBe(`${(1200).toLocaleString()} rows answered, 3 couldn't be`);
    expect(statusText(create(ScoringSchema, { status: JobStatus.RUNNING }))).toBe("Reading the rows");
  });
});
