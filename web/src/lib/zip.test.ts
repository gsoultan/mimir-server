import { expect, test } from "bun:test";
import { crc32, pathOf, zip } from "./zip";

test("crc32 matches the known value", () => {
  expect(crc32(new TextEncoder().encode("123456789"))).toBe(0xcbf43926);
});

test.skipIf(!Bun.which("unzip"))("zip stores files that unzip back", async () => {
  const out = zip([
    { path: "fruit/apple/1.jpg", data: new Uint8Array([1, 2, 3]) },
    { path: "fruit/pear/2.jpg", data: new Uint8Array([4, 5]) },
  ]);
  const dir = await Bun.$`mktemp -d`.text();
  const file = `${dir.trim()}/x.zip`;
  await Bun.write(file, out);
  const list = await Bun.$`unzip -l ${file}`.text();
  expect(list).toContain("fruit/apple/1.jpg");
  expect(list).toContain("fruit/pear/2.jpg");
  const test = await Bun.$`unzip -t ${file}`.text();
  expect(test).toContain("No errors detected");
});

test("pathOf prefers the folder path", () => {
  const f = new File([""], "1.jpg");
  Object.defineProperty(f, "path", { value: "./fruit/apple/1.jpg" });
  expect(pathOf(f)).toBe("fruit/apple/1.jpg");
  expect(pathOf(new File([""], "2.jpg"))).toBe("2.jpg");
});
