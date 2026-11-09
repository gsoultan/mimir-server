import { describe, expect, test } from "bun:test";

import { clock, fitPoints, planDays } from "./visits";

describe("visits", () => {
  test("clock", () => {
    expect(clock(9 * 60 + 5)).toBe("09:05");
    expect(clock(17 * 60)).toBe("17:00");
  });
  test("planDays", () => {
    expect(planDays({ startDate: "", endDate: "" })).toBe("");
    expect(planDays({ startDate: "2026-11-09", endDate: "2026-11-09" })).not.toContain("to");
    expect(planDays({ startDate: "2026-11-09", endDate: "2026-11-15" })).toContain(" to ");
  });
  test("fitPoints keeps every place in the box", () => {
    const pts = [{ lat: -6.2, lon: 106.8 }, { lat: -6.3, lon: 106.9 }, { lat: -6.25, lon: 106.85 }];
    const at = fitPoints(pts, 300, 200);
    const [a, b] = [pts[0]!, pts[1]!];
    for (const p of pts) {
      const [x, y] = at(p);
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThanOrEqual(300);
      expect(y).toBeGreaterThanOrEqual(0);
      expect(y).toBeLessThanOrEqual(200);
    }
    // North is up.
    expect(at(a)[1]).toBeLessThan(at(b)[1]);
  });
  test("one place sits in the middle", () => {
    const at = fitPoints([{ lat: 1, lon: 1 }], 100, 100);
    const [x, y] = at({ lat: 1, lon: 1 });
    expect(Math.round(x)).toBe(50);
    expect(Math.round(y)).toBe(50);
  });
});

import { create } from "@bufbuild/protobuf";
import { VisitPlanSchema } from "../api/gen/mimir/v1/visit_pb";
import { repeatOf, repeatRequest, repeatText } from "./visits";

describe("repeat", () => {
  test("a schedule reads back and says when", () => {
    const p = create(VisitPlanSchema, { repeatSeconds: 86400n, schedule: { atMinute: 330, weekdays: [1, 4] } });
    expect(repeatText(p)).toBe("05:30 on Mon, Thu");
    expect(repeatOf(p)).toEqual({ mode: "schedule", atMinute: 330, weekdays: [1, 4] });
    expect(repeatRequest(repeatOf(p))).toEqual({ repeatSeconds: 0n, schedule: { atMinute: 330, weekdays: [1, 4] } });
  });
  test("every day from now, and once", () => {
    expect(repeatText(create(VisitPlanSchema, { repeatSeconds: 86400n }))).toBe("Every day, from now");
    expect(repeatText(create(VisitPlanSchema, {}))).toBe("");
    expect(repeatRequest({ mode: "0", atMinute: 0, weekdays: [] })).toEqual({ repeatSeconds: 0n });
  });
});
