import { describe, expect, test } from "vitest";
import { assertTeam, hotAlias, monthlyIndex, parseIndexName, teamIndexPattern } from "../src/naming.js";

describe("naming", () => {
  test("월별 인덱스와 alias", () => {
    expect(monthlyIndex("ads", new Date("2026-03-31T23:00:00Z"))).toBe("mem-ads-2026.03");
    expect(hotAlias("ads")).toBe("mem-ads-hot");
    expect(teamIndexPattern("ads")).toBe("mem-ads-*");
  });

  test("인덱스 이름을 파싱한다", () => {
    expect(parseIndexName("mem-ads-data-2026.12")).toEqual({
      team: "ads-data",
      monthStart: new Date("2026-12-01T00:00:00Z"),
      monthEnd: new Date("2027-01-01T00:00:00Z"),
    });
    expect(parseIndexName("mem-ads-hot")).toBeNull();
  });

  test("잘못된 team", () => {
    expect(() => assertTeam("Ads")).toThrow();
    expect(() => assertTeam("-x")).toThrow();
  });
});
