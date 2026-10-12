import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import path from "node:path";
import type { JudgeSummary } from "./judge.js";
import type { CycleSummary } from "./reconcile.js";

export interface CycleRecord {
  readonly trigger: string;
  readonly at: string;
  readonly summary: CycleSummary | null;
  readonly judge: JudgeSummary | null;
  readonly error: string | null;
}

/** 데몬이 사이클마다 쓰는 상태. daemon-status 가 읽는다 */
export interface DaemonStatus {
  readonly pid: number;
  readonly startedAt: string;
  readonly heartbeatAt: string;
  readonly intervalSeconds: number;
  readonly cycles: number;
  readonly lastCycle: CycleRecord | null;
  readonly distillPending: number;
}

export function daemonDir(root: string): string {
  return path.join(root, ".data", "daemon");
}

const statusFile = (root: string) => path.join(daemonDir(root), "status.json");
const pidFile = (root: string) => path.join(daemonDir(root), "daemon.pid");

export async function writeStatus(root: string, status: DaemonStatus): Promise<void> {
  await mkdir(daemonDir(root), { recursive: true });
  const tmp = `${statusFile(root)}.tmp`;
  await writeFile(tmp, JSON.stringify(status, null, 2), "utf8");
  // rename 으로 바꿔 끼워 읽는 쪽이 반쯤 쓴 파일을 보지 않게 한다
  await rename(tmp, statusFile(root));
}

export async function readStatus(root: string): Promise<DaemonStatus | null> {
  try {
    return JSON.parse(await readFile(statusFile(root), "utf8")) as DaemonStatus;
  } catch {
    return null;
  }
}

export function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

export async function runningDaemonPid(root: string): Promise<number | null> {
  try {
    const pid = Number((await readFile(pidFile(root), "utf8")).trim());
    return Number.isInteger(pid) && pid > 0 && isAlive(pid) ? pid : null;
  } catch {
    return null;
  }
}

/** 데몬은 하나만 — 파생물의 writer 가 둘이면 수렴이 깨진다. 이미 떠 있으면 그 pid 를 돌려준다 */
export async function acquireDaemonLock(root: string): Promise<{ acquired: true } | { acquired: false; pid: number }> {
  const existing = await runningDaemonPid(root);
  if (existing !== null && existing !== process.pid) return { acquired: false, pid: existing };
  await mkdir(daemonDir(root), { recursive: true });
  await writeFile(pidFile(root), String(process.pid), "utf8");
  return { acquired: true };
}

export async function releaseDaemonLock(root: string): Promise<void> {
  if ((await runningDaemonPid(root)) === process.pid) await rm(pidFile(root), { force: true });
}
