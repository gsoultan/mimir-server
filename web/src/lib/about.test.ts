import { describe, expect, test } from "bun:test";

import { details, workers } from "./about";

describe("workers", () => {
  test("none", () => expect(workers({ version: "0.2.0", workerVersions: [] })).toBe("None connected"));
  test("all on the server's version", () => expect(workers({ version: "0.2.0", workerVersions: ["v0.2.0", "0.2.0"] })).toBe("2 connected · all on 0.2.0"));
  test("some behind", () => expect(workers({ version: "0.2.0", workerVersions: ["0.2.0", "0.1.9"] })).toBe("2 connected · 1 on another version"));
});

test("details reads as a bug report", () => {
  const text = details({ version: "0.2.0", commit: "7f3c2a1e9b", modified: false, goVersion: "1.27.3", schemaVersion: 42, workerVersions: [], parts: [{ path: "github.com/gsoultan/mimir", version: "0.4.2" } as never] });
  expect(text).toBe("mimir-server 0.2.0\ncommit 7f3c2a1e9b\nGo 1.27.3\ndatabase schema 42\nworkers: None connected\ngithub.com/gsoultan/mimir 0.4.2");
});
