import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, test } from "vitest";
import { isProbablyBinary, scanHot } from "../src/daemon/scan.js";

describe("scanHot", () => {
  test("색인 대상만 stat 과 함께, 크기 초과는 사유와 함께 건너뛴다", async () => {
    const root = await mkdtemp(path.join(os.tmpdir(), "scan-"));
    await mkdir(path.join(root, "docs/a"), { recursive: true });
    await mkdir(path.join(root, "main/svc/docs"), { recursive: true });
    await mkdir(path.join(root, "main/svc/src"), { recursive: true });
    await mkdir(path.join(root, "app"), { recursive: true });
    await writeFile(path.join(root, "README.md"), "# r");
    await writeFile(path.join(root, "docs/a/x.md"), "# x");
    await writeFile(path.join(root, "docs/a/big.md"), "x".repeat(200));
    await writeFile(path.join(root, "main/svc/docs/y.md"), "# y");
    await writeFile(path.join(root, "main/svc/src/z.md"), "# no");
    await writeFile(path.join(root, "app/README.md"), "# no");

    const { files, skipped } = await scanHot(root, 100);
    expect(files.map((f) => f.rel)).toEqual(["README.md", "docs/a/x.md", "main/svc/docs/y.md"]);
    expect(files[0]).toMatchObject({ size: 3 });
    expect(skipped).toEqual([{ rel: "docs/a/big.md", reason: "size 200 > 100" }]);
  });
});

describe("isProbablyBinary", () => {
  test("NUL 바이트가 있으면 바이너리", async () => {
    const dir = await mkdtemp(path.join(os.tmpdir(), "bin-"));
    await writeFile(path.join(dir, "a.md"), Buffer.from([0x23, 0x00, 0x41]));
    await writeFile(path.join(dir, "b.md"), "# 텍스트");
    expect(await isProbablyBinary(path.join(dir, "a.md"))).toBe(true);
    expect(await isProbablyBinary(path.join(dir, "b.md"))).toBe(false);
  });
});
