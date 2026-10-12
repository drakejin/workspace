import path from "node:path";
import { z } from "zod";

const emptyToUndefined = (v: unknown) => (v === "" ? undefined : v);
const optionalString = z.preprocess(emptyToUndefined, z.string().optional());

const ConfigSchema = z.object({
  opensearchUrl: z.string().url(),
  metaRepoRoot: z.string().min(1),
  defaultTeam: z.string().regex(/^[a-z0-9][a-z0-9-]*$/, "team은 소문자/숫자/하이픈만"),
  hotDays: z.coerce.number().int().positive(),
  snapshot: z.object({
    repository: z.string().min(1),
    type: z.enum(["fs", "s3"]),
    fsLocation: z.string().min(1),
    s3Bucket: optionalString,
  }),
  reconcile: z.object({
    intervalSeconds: z.coerce.number().int().positive(),
    debounceMs: z.coerce.number().int().nonnegative(),
    maxFileBytes: z.coerce.number().int().positive(),
    sanityMaxFiles: z.coerce.number().int().positive(),
  }),
  judge: z.object({
    enabled: z.boolean(),
    windowDays: z.coerce.number().positive(),
    minScore: z.coerce.number().min(0).max(1),
  }),
  distill: z.object({
    enabled: z.boolean(),
    concurrency: z.coerce.number().int().positive(),
  }),
  llm: z.object({
    claudeBin: z.string().min(1),
    model: z.string().min(1),
    timeoutMs: z.coerce.number().int().positive(),
  }),
  coldStore: z.object({
    dir: z.string().min(1),
    s3Bucket: optionalString,
    s3Prefix: z.string(),
    region: z.string().min(1),
  }),
});

export type MemoryConfig = z.infer<typeof ConfigSchema>;

function flag(value: string | undefined, fallback: boolean): boolean {
  if (value === undefined || value === "") return fallback;
  return ["1", "true", "yes", "on"].includes(value.toLowerCase());
}

/** 환경변수에서 설정을 읽고 검증한다. 잘못된 값이면 바로 실패한다. */
export function loadConfig(env: NodeJS.ProcessEnv = process.env): MemoryConfig {
  // pnpm --filter 는 스크립트를 앱 디렉토리에서 돌리므로, 명령을 친 위치(INIT_CWD)를 먼저 본다
  const rootInput = emptyToUndefined(env.META_REPO_ROOT) ?? emptyToUndefined(env.INIT_CWD) ?? process.cwd();
  const metaRepoRoot = path.resolve(rootInput as string);
  const parsed = ConfigSchema.safeParse({
    opensearchUrl: emptyToUndefined(env.OPENSEARCH_URL) ?? "http://localhost:9202",
    metaRepoRoot,
    defaultTeam: emptyToUndefined(env.MEMORY_DEFAULT_TEAM) ?? "default",
    hotDays: emptyToUndefined(env.MEMORY_HOT_DAYS) ?? 60,
    snapshot: {
      repository: emptyToUndefined(env.COLD_SNAPSHOT_REPOSITORY) ?? "mem-cold",
      type: emptyToUndefined(env.COLD_SNAPSHOT_TYPE) ?? "fs",
      fsLocation: emptyToUndefined(env.COLD_SNAPSHOT_FS_LOCATION) ?? "/usr/share/opensearch/snapshots",
      s3Bucket: env.COLD_SNAPSHOT_S3_BUCKET,
    },
    reconcile: {
      intervalSeconds: emptyToUndefined(env.MEMORY_RECONCILE_INTERVAL_SECONDS) ?? 60,
      debounceMs: emptyToUndefined(env.MEMORY_RECONCILE_DEBOUNCE_MS) ?? 3000,
      maxFileBytes: emptyToUndefined(env.MEMORY_MAX_FILE_BYTES) ?? 2 * 1024 * 1024,
      sanityMaxFiles: emptyToUndefined(env.MEMORY_SANITY_MAX_FILES) ?? 5000,
    },
    judge: {
      enabled: flag(env.MEMORY_JUDGE_ENABLED, true),
      windowDays: emptyToUndefined(env.MEMORY_JUDGE_WINDOW_DAYS) ?? 7,
      // more_like_this 점수 / 자기 자신 점수. 짧은 문서는 같은 주제여도 0.4 안팎이 나온다
      minScore: emptyToUndefined(env.MEMORY_JUDGE_MIN_SCORE) ?? 0.3,
    },
    // 증류는 문서 본문이 LLM 으로 간다. 기밀 범위를 정하기 전엔 꺼 둔다
    distill: {
      enabled: flag(env.MEMORY_DISTILL_ENABLED, false),
      concurrency: emptyToUndefined(env.MEMORY_DISTILL_CONCURRENCY) ?? 2,
    },
    llm: {
      claudeBin: emptyToUndefined(env.MEMORY_CLAUDE_BIN) ?? "claude",
      model: emptyToUndefined(env.MEMORY_LLM_MODEL) ?? "opus",
      timeoutMs: emptyToUndefined(env.MEMORY_LLM_TIMEOUT_MS) ?? 120_000,
    },
    coldStore: {
      dir: path.resolve(metaRepoRoot, emptyToUndefined(env.COLD_STORE_DIR) as string | undefined ?? ".data/cold-store"),
      s3Bucket: env.COLD_STORE_S3_BUCKET,
      s3Prefix: env.COLD_STORE_S3_PREFIX ?? "memory",
      region: emptyToUndefined(env.AWS_REGION) ?? "ap-northeast-2",
    },
  });
  if (!parsed.success) {
    const detail = parsed.error.issues.map((i) => `${i.path.join(".")}: ${i.message}`).join("; ");
    throw new Error(`memory 설정이 잘못됐습니다 — ${detail}`);
  }
  if (parsed.data.snapshot.type === "s3" && !parsed.data.snapshot.s3Bucket) {
    throw new Error("COLD_SNAPSHOT_TYPE=s3 이면 COLD_SNAPSHOT_S3_BUCKET 이 필요합니다");
  }
  return parsed.data;
}
