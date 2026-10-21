import { create } from "@bufbuild/protobuf";
import { expect, test } from "bun:test";

import { ColumnSchema, ModelSchema } from "../api/gen/mimir/v1/model_pb";
import { device } from "./device-shared";

// device sends the worker what internal/device.Model reads, by its JSON
// names; a mismatch would make every column read as empty.
test("device describes a model the way the browser runtime reads it", () => {
  const m = create(ModelSchema, {
    id: "m1",
    version: "abc",
    name: "Who renews",
    architecture: "table-mlp",
    inputShape: [3n],
    labels: ["no", "yes"],
    columns: [create(ColumnSchema, { name: "plan", kind: "category", values: ["basic", "pro"] }), create(ColumnSchema, { name: "seats", kind: "number", fill: 12 })],
  });
  expect(device(m)).toEqual({
    id: "m1",
    version: "abc",
    architecture: "table-mlp",
    inputShape: [3],
    labels: ["no", "yes"],
    features: [],
    columns: [{ name: "plan", kind: "category", values: ["basic", "pro"], fill: 0 }, { name: "seats", kind: "number", values: [], fill: 12 }],
  });
});
