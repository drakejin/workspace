export interface SerialRunner {
  /** 돌고 있으면 재실행 1회만 예약한다. 예약된 재실행까지 끝나면 resolve */
  run(reason: string): Promise<void>;
  idle(): Promise<void>;
}

/** 트리거가 몰려도 작업은 항상 직렬 1개 + 최대 1회 재실행 */
export function createSerialRunner(task: (reason: string) => Promise<void>): SerialRunner {
  let current: Promise<void> | null = null;
  let rerun: Promise<void> | null = null;

  const start = (reason: string): Promise<void> => {
    current = task(reason).finally(() => {
      current = null;
    });
    return current;
  };

  return {
    run(reason) {
      if (!current) return start(reason);
      rerun ??= current.then(() => {
        rerun = null;
        return start(reason);
      });
      return rerun;
    },
    async idle() {
      while (current || rerun) await (rerun ?? current);
    },
  };
}
