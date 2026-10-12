import { mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import {
  DeleteObjectCommand,
  GetObjectCommand,
  ListObjectsV2Command,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import type { MemoryConfig } from "../config.js";

/** raw 원본과 청크 JSONL을 두는 곳. 로컬 디렉토리 또는 S3. */
export interface ColdStore {
  readonly location: string;
  put(key: string, body: string): Promise<void>;
  get(key: string): Promise<string | null>;
  list(prefix: string): Promise<string[]>;
  remove(key: string): Promise<void>;
}

export function createColdStore(config: MemoryConfig): ColdStore {
  const { s3Bucket, s3Prefix, region, dir } = config.coldStore;
  return s3Bucket ? s3ColdStore(new S3Client({ region }), s3Bucket, s3Prefix) : localColdStore(dir);
}

function assertSafeKey(key: string): string {
  const normalized = path.posix.normalize(key);
  if (normalized.startsWith("..") || path.posix.isAbsolute(normalized)) {
    throw new Error(`cold store 키가 잘못됐습니다: ${key}`);
  }
  return normalized;
}

export function localColdStore(root: string): ColdStore {
  const resolve = (key: string) => path.join(root, assertSafeKey(key));
  return {
    location: root,
    async put(key, body) {
      const file = resolve(key);
      await mkdir(path.dirname(file), { recursive: true });
      await writeFile(file, body, "utf8");
    },
    async get(key) {
      try {
        return await readFile(resolve(key), "utf8");
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT") return null;
        throw error;
      }
    },
    async list(prefix) {
      const base = resolve(prefix);
      try {
        const entries = await readdir(base, { recursive: true, withFileTypes: true });
        return entries
          .filter((e) => e.isFile())
          .map((e) => path.relative(root, path.join(e.parentPath, e.name)).split(path.sep).join("/"))
          .sort();
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT") return [];
        throw error;
      }
    },
    async remove(key) {
      await rm(resolve(key), { force: true });
    },
  };
}

export function s3ColdStore(s3: S3Client, bucket: string, prefix: string): ColdStore {
  const full = (key: string) => path.posix.join(prefix, assertSafeKey(key));
  return {
    location: `s3://${bucket}/${prefix}`,
    async put(key, body) {
      await s3.send(new PutObjectCommand({ Bucket: bucket, Key: full(key), Body: body }));
    },
    async get(key) {
      try {
        const out = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: full(key) }));
        return (await out.Body?.transformToString("utf8")) ?? null;
      } catch (error) {
        if ((error as { name?: string }).name === "NoSuchKey") return null;
        throw error;
      }
    },
    async list(listPrefix) {
      const keys: string[] = [];
      let token: string | undefined;
      do {
        const out = await s3.send(
          new ListObjectsV2Command({ Bucket: bucket, Prefix: full(listPrefix), ContinuationToken: token }),
        );
        keys.push(...(out.Contents ?? []).map((o) => path.posix.relative(prefix, o.Key ?? "")));
        token = out.IsTruncated ? out.NextContinuationToken : undefined;
      } while (token);
      return keys.sort();
    },
    async remove(key) {
      await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: full(key) }));
    },
  };
}

export const coldKeys = {
  raw: (team: string, sourcePath: string) => `${team}/raw/${sourcePath}`,
  chunks: (team: string, sourcePath: string) => `${team}/chunks/${sourcePath}.jsonl`,
  chunksPrefix: (team: string) => `${team}/chunks/`,
};
