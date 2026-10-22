import { expect, test } from "bun:test";

import { defaultPostgres, newSecretKey, postgresReady, postgresUrl } from "./postgres";

test("postgresUrl escapes each part", () => {
  expect(postgresUrl({ ...defaultPostgres, password: "p@ss:w/rd?#" })).toBe("postgres://mimir:p%40ss%3Aw%2Frd%3F%23@localhost:5432/mimir?sslmode=prefer");
  expect(postgresUrl({ ...defaultPostgres, host: "::1", ssl: "disable" })).toBe("postgres://mimir@[::1]:5432/mimir?sslmode=disable");
  expect(postgresUrl({ ...defaultPostgres, host: " db.internal ", user: "app user", database: "my db", port: 6543 })).toBe("postgres://app%20user@db.internal:6543/my%20db?sslmode=prefer");
});

test("postgresReady needs a host, user, database and a real port", () => {
  expect(postgresReady(defaultPostgres)).toBe(true);
  expect(postgresReady({ ...defaultPostgres, host: " " })).toBe(false);
  expect(postgresReady({ ...defaultPostgres, port: 70000 })).toBe(false);
});

test("newSecretKey is 32 random bytes as base64", () => {
  const k = newSecretKey();
  expect(atob(k).length).toBe(32);
  expect(newSecretKey()).not.toBe(k);
});
