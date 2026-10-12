import { describe, expect, test } from "vitest";
import { createSerialRunner } from "../src/serial.js";

describe("createSerialRunner", () => {
  test("돌고 있는 동안의 트리거는 재실행 1회로 합쳐지고, 동시에 둘이 돌지 않는다", async () => {
    const runs: string[] = [];
    let active = 0;
    let maxActive = 0;
    let release: () => void = () => {};
    const runner = createSerialRunner(async (reason) => {
      active++;
      maxActive = Math.max(maxActive, active);
      runs.push(reason);
      if (reason === "boot") await new Promise<void>((r) => (release = r));
      active--;
    });
    const boot = runner.run("boot");
    const a = runner.run("watch");
    const b = runner.run("timer");
    expect(a).toBe(b);
    release();
    await Promise.all([boot, a, b]);
    await runner.idle();
    expect(runs).toEqual(["boot", "watch"]);
    expect(maxActive).toBe(1);
  });

  test("실패해도 다음 실행은 된다", async () => {
    const runs: string[] = [];
    const runner = createSerialRunner(async (r) => {
      runs.push(r);
      if (r === "x") throw new Error("boom");
    });
    await expect(runner.run("x")).rejects.toThrow("boom");
    await runner.run("y");
    expect(runs).toEqual(["x", "y"]);
  });
});
