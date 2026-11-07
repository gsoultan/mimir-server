import { describe as group, expect, test } from "bun:test";

import { answer, describe, guess, parse, profile, tips } from "./csv";

group("parse", () => {
  test("reads quotes, embedded commas and line breaks", () => {
    const p = parse('﻿name,note,amount\r\n"Smith, Jo","said ""hi""\nthen left",1200\r\nLee,,"1,500"\r\n');
    expect(p.columns).toEqual(["name", "note", "amount"]);
    expect(p.rows).toEqual([["Smith, Jo", 'said "hi"\nthen left', "1200"], ["Lee", "", "1,500"]]);
  });
  test("drops a row cut short and pads short rows", () => {
    const p = parse("a,b\n1,2\n3", { cut: true });
    expect(p.rows).toEqual([["1", "2"]]);
    expect(parse("a,b\n1\n").rows).toEqual([["1", ""]]);
    expect(parse("a,b\n1,2\n3,4\n5,6\n", { maxRows: 2 }).rows.length).toBe(2);
  });
});

const table = (n: number) => parse(["customer_id,plan,spend,joined,note,left", ...Array.from({ length: n }, (_, i) =>
  `C-${i},${["basic", "pro", "team"][i % 3]},${(i * 7.5).toFixed(1)},2026-0${1 + (i % 9)}-1${i % 10},${i % 2 ? "called about billing twice" : ""},${i % 4 === 0 ? "yes" : "no"}`)].join("\n"));

group("profile", () => {
  const cols = profile(table(120));
  test("tells ids, groups, numbers, dates, text and yes/no apart", () => {
    expect(cols.map((c) => c.kind)).toEqual(["id", "category", "number", "date", "text", "yesno"]);
    expect(cols[1]!.distinct).toBe(3);
    expect(cols[2]!.min).toBe(0);
    expect(cols[4]!.filled).toBe(60);
    expect(describe(cols[1]!)).toBe("one of 3 values");
    expect(describe(cols[0]!)).toBe("an ID, different in every row");
  });
  test("guesses the last column that can be an answer", () => {
    expect(guess(cols)).toBe("left");
    expect(guess(profile(parse("a,b,order_id\n" + Array.from({ length: 30 }, (_, i) => `${i % 2},${i % 3},${i}`).join("\n"))))).toBe("b");
    expect(guess(cols, "value")).toBe("spend");
  });
  test("says what each column would answer", () => {
    expect(answer(cols[5]!).what).toBe("yes or no");
    expect(answer(cols[5]!).level).toBeUndefined();
    expect(answer(cols[1]!).what).toBe("one of 3 groups");
    expect(answer(cols[2]!).what).toBe("a number");
    expect(answer(cols[0]!).level).toBe("bad");
    expect(answer(cols[3]!).level).toBe("bad");
    expect(answer(cols[4]!).level).toBe("bad");
    expect(answer(cols[2]!, "value").level).toBeUndefined();
    expect(answer(cols[1]!, "value").level).toBe("bad");
  });
  test("warns about rare answers and too many of them", () => {
    const rare = profile(parse("x,y\n" + Array.from({ length: 200 }, (_, i) => `${i},${i === 0 ? "yes" : "no"}`).join("\n")));
    expect(answer(rare[1]!).level).toBe("warn");
    const many = profile(parse("x,y\n" + Array.from({ length: 200 }, (_, i) => `${i % 2},v${i % 80}`).join("\n")));
    expect(answer(many[1]!).level).toBe("warn");
  });
  test("spots one answer spelled several ways", () => {
    const cols = profile(parse("x,y\n" + Array.from({ length: 60 }, (_, i) => `${i % 3},${["yes", "Yes", "no", "no "][i % 4]}`).join("\n")));
    expect(cols[1]!.alike).toEqual([["yes", "Yes"]]);
    expect(answer(cols[1]!).level).toBe("warn");
    expect(answer(cols[1]!).detail).toContain("“yes” and “Yes”");
  });
  test("tips on columns that tell the model nothing", () => {
    expect(tips(cols, "left").map((t) => t.column)).toEqual(["customer_id"]);
    const flat = profile(parse("a,b,c\n" + Array.from({ length: 30 }, (_, i) => `x,,${i % 2}`).join("\n")));
    expect(tips(flat, "c").map((t) => t.column)).toEqual(["a", "b"]);
  });
});
