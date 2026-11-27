import { describe, expect, test } from "bun:test";

import { GetModelTrialResponseSchema, ModelTrialSchema, TrialSideSchema } from "../api/gen/mimir/v1/model_pb";
import { create } from "@bufbuild/protobuf";
import { trialHeadline, trialVerdict } from "./trial";

const side = (versionNumber: number, answers: number, feedback: number, accuracy: number) =>
  create(TrialSideSchema, { versionNumber, answers: BigInt(answers), feedback: BigInt(feedback), accuracy });

describe("trial", () => {
  test("headline", () => {
    expect(trialHeadline(create(ModelTrialSchema, { share: 10 }), 3)).toContain("10% of the questions");
    expect(trialHeadline(create(ModelTrialSchema, { shadow: true }), 3)).toContain("copy of every question");
  });
  test("verdict waits for feedback", () => {
    const r = create(GetModelTrialResponseSchema, { trial: create(ModelTrialSchema, { shadow: true }), live: side(2, 40, 0, 0), tried: side(3, 40, 0, 0), agreeShare: 0.9 });
    const v = trialVerdict(r, false);
    expect(v.tone).toBe("wait");
    expect(v.text).toContain("90 of 100");
  });
  test("verdict compares accuracy", () => {
    const base = { trial: create(ModelTrialSchema, { share: 10 }), enoughFeedback: true };
    expect(trialVerdict(create(GetModelTrialResponseSchema, { ...base, live: side(2, 90, 30, 0.8), tried: side(3, 10, 25, 0.85) }), false).tone).toBe("ok");
    expect(trialVerdict(create(GetModelTrialResponseSchema, { ...base, live: side(2, 90, 30, 0.9), tried: side(3, 10, 25, 0.7) }), true)).toEqual(expect.objectContaining({ tone: "warn" }));
  });
});
