import { mkdtemp } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { describe, expect, test } from "vitest";
import { coldKeys, localColdStore } from "../src/cold/cold-store.js";

describe("localColdStore", () => {
  test("put/get/list/remove", async () => {
    const store = localColdStore(await mkdtemp(path.join(os.tmpdir(), "cold-")));
    await store.put(coldKeys.chunks("ads", "docs/a.md"), "{}\n");
    await store.put(coldKeys.raw("ads", "docs/a.md"), "# a");
    expect(await store.get("ads/raw/docs/a.md")).toBe("# a");
    expect(await store.list(coldKeys.chunksPrefix("ads"))).toEqual(["ads/chunks/docs/a.md.jsonl"]);
    expect(await store.list("nope/")).toEqual([]);
    await store.remove("ads/raw/docs/a.md");
    expect(await store.get("ads/raw/docs/a.md")).toBeNull();
  });

  test("밖으로 나가는 키는 거부", async () => {
    const store = localColdStore(await mkdtemp(path.join(os.tmpdir(), "cold-")));
    await expect(store.put("../x", "")).rejects.toThrow(/키가 잘못/);
  });
});

describe("s3ColdStore", () => {
  test("prefix 아래로 put/get/list(페이지네이션)/remove, 없는 키는 null", async () => {
    const { s3ColdStore } = await import("../src/cold/cold-store.js");
    const objects = new Map<string, string>();
    const sent: string[] = [];
    const fake = {
      send: async (cmd: { constructor: { name: string }; input: Record<string, unknown> }) => {
        const name = cmd.constructor.name;
        sent.push(name);
        const key = cmd.input.Key as string;
        if (name === "PutObjectCommand") return void objects.set(key, cmd.input.Body as string);
        if (name === "DeleteObjectCommand") return void objects.delete(key);
        if (name === "GetObjectCommand") {
          if (!objects.has(key)) throw Object.assign(new Error("nope"), { name: "NoSuchKey" });
          return { Body: { transformToString: async () => objects.get(key) } };
        }
        // ListObjectsV2: 한 번에 한 개씩 돌려줘서 페이지네이션을 태운다
        const keys = [...objects.keys()].filter((k) => k.startsWith(cmd.input.Prefix as string)).sort();
        const start = Number(cmd.input.ContinuationToken ?? 0);
        return { Contents: keys.slice(start, start + 1).map((Key) => ({ Key })), IsTruncated: start + 1 < keys.length, NextContinuationToken: String(start + 1) };
      },
    };
    const store = s3ColdStore(fake as never, "bucket", "memory");
    expect(store.location).toBe("s3://bucket/memory");
    await store.put("ads/chunks/a.jsonl", "1");
    await store.put("ads/chunks/b.jsonl", "2");
    expect([...objects.keys()]).toEqual(["memory/ads/chunks/a.jsonl", "memory/ads/chunks/b.jsonl"]);
    expect(await store.get("ads/chunks/b.jsonl")).toBe("2");
    expect(await store.get("missing")).toBeNull();
    expect(await store.list("ads/chunks/")).toEqual(["ads/chunks/a.jsonl", "ads/chunks/b.jsonl"]);
    await store.remove("ads/chunks/a.jsonl");
    expect(await store.list("ads/")).toEqual(["ads/chunks/b.jsonl"]);
    await expect(store.put("../escape", "")).rejects.toThrow();
  });
});
