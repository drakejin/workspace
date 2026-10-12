import { describe, expect, test } from "vitest";
import { parseJsonl } from "../src/cold/reload.js";

describe("parseJsonl", () => {
  test("깨진 줄은 건너뛰고 줄 번호를 남긴다", () => {
    const { parsed, badLines } = parseJsonl('{"id":"a"}\n{broken\n\n{"id":"b"}\n');
    expect(parsed.map((c) => c.id)).toEqual(["a", "b"]);
    expect(badLines).toEqual([2]);
  });
});
