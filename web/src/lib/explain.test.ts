import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";

import { ArchitectureSchema, ColumnSchema, ContributionSchema, ModelSchema } from "../api/gen/mimir/v1/model_pb";
import { answerText, because, effectText, explains } from "./explain";

describe("explain", () => {
  test("which models can be explained", () => {
    const table = create(ArchitectureSchema, { input: "table", output: "class" });
    const withCols = create(ModelSchema, { columns: [create(ColumnSchema, { name: "bill", kind: "number" })] });
    expect(explains(withCols, table)).toBe(true);
    expect(explains(create(ModelSchema, {}), table)).toBe(false);
    expect(explains(withCols, create(ArchitectureSchema, { input: "table", output: "ranked" }))).toBe(false);
    expect(explains(withCols, create(ArchitectureSchema, { input: "image", output: "class" }))).toBe(false);
  });
  test("effects in words", () => {
    expect(effectText(0.183, false)).toBe("+18 pts");
    expect(effectText(-0.05, false)).toBe("−5 pts");
    expect(effectText(0.002, false)).toBe("+<1 pts");
    expect(effectText(-12.5, true)).toBe("−12.5");
    const c = create(ContributionSchema, { column: "monthly_bill", value: "95", typical: "45", effect: 0.18 });
    expect(because(c, "yes", false)).toBe("monthly_bill of 95 raised the chance of yes by 18 points, compared with 45.");
    expect(answerText(0.734, false)).toBe("73%");
  });
});
