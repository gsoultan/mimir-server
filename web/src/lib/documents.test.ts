import { create } from "@bufbuild/protobuf";
import { describe, expect, test } from "bun:test";

import { DocumentSchema } from "../api/gen/mimir/v1/document_pb";
import { JobStatus } from "../api/gen/mimir/v1/training_pb";
import { cited, documentStatus, readable, reading } from "./documents";

describe("cited", () => {
  test("splits out citations of the sources given", () => {
    expect(cited("Parcels leave daily [1]. Returns take 30 days [2, 3].", 3)).toEqual([
      { text: "Parcels leave daily " }, { source: 1 }, { text: ". Returns take 30 days " }, { source: 2 }, { source: 3 }, { text: "." },
    ]);
  });
  test("leaves numbers beyond the sources as text", () => {
    expect(cited("See [4] and [1]", 2)).toEqual([{ text: "See [4] and " }, { source: 1 }]);
    expect(cited("", 2)).toEqual([]);
  });
});

describe("documents", () => {
  test("which files can be read", () => {
    expect(readable("Policy.PDF")).toBe(true);
    expect(readable("notes.md")).toBe(true);
    expect(readable("photo.png")).toBe(false);
  });
  test("status in words", () => {
    expect(documentStatus(create(DocumentSchema, { status: JobStatus.SUCCEEDED, passages: 1n }))).toBe("1 passage");
    expect(documentStatus(create(DocumentSchema, { status: JobStatus.SUCCEEDED, passages: 12n }))).toBe("12 passages");
    expect(reading(create(DocumentSchema, { status: JobStatus.QUEUED }))).toBe(true);
    expect(reading(create(DocumentSchema, { status: JobStatus.FAILED }))).toBe(false);
  });
});
