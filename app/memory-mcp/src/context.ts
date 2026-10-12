import {
  bootstrap,
  createColdStore,
  createIndexer,
  createOpenSearchPort,
  loadConfig,
  type ColdStore,
  type Indexer,
  type MemoryConfig,
  type OpenSearchPort,
} from "@meta/memory-core";

export interface AppContext {
  readonly config: MemoryConfig;
  readonly os: OpenSearchPort;
  readonly coldStore: ColdStore;
  readonly indexer: Indexer;
  /** 템플릿·ISM·스냅샷 repository를 한 번 맞춘다. 실패하면 다음 호출에서 다시 시도한다. */
  readonly ready: () => Promise<void>;
}

export function createContext(config: MemoryConfig = loadConfig()): AppContext {
  const os = createOpenSearchPort(config.opensearchUrl);
  const coldStore = createColdStore(config);
  const indexer = createIndexer({ os, coldStore, config });
  let pending: Promise<void> | null = null;
  const ready = () => {
    pending ??= bootstrap(os, config).catch((error: unknown) => {
      pending = null;
      throw error;
    });
    return pending;
  };
  return { config, os, coldStore, indexer, ready };
}
