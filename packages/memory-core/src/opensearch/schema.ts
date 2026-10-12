import { INDEX_PREFIX } from "../naming.js";

export const INDEX_TEMPLATE_NAME = "mem-template";
export const ISM_POLICY_ID = "mem-hot-cold";
export const AUTO_MANAGE_SETTING = "index.plugins.index_state_management.auto_manage";

/**
 * 식별자 친화 analyzer. `ClickDeduplicator.window`, `click_dedup_window` 같은 식별자를
 * 원형과 조각(click, deduplicator, window) 모두로 색인해 BM25가 함수명·필드명으로도 맞춘다.
 */
const IDENTIFIER_SPLIT = {
  type: "word_delimiter_graph",
  preserve_original: true,
  split_on_case_change: true,
  split_on_numerics: false,
  stem_english_possessive: false,
};

export const TEXT_ANALYZER = "memory_text";
export const SEARCH_ANALYZER = "memory_search";

/** 데몬(reconcile·판정·증류)이 쓰는 필드. 기존 인덱스에도 put mapping 으로 추가한다 */
export const DAEMON_FIELDS = {
  content_sha: { type: "keyword" },
  body_sha: { type: "keyword" },
  mtime_ms: { type: "long" },
  size: { type: "long" },
  title: { type: "text", analyzer: TEXT_ANALYZER, search_analyzer: SEARCH_ANALYZER },
  supersedes: { type: "keyword" },
  superseded_by: { type: "keyword" },
  summary: { type: "text", analyzer: TEXT_ANALYZER, search_analyzer: SEARCH_ANALYZER },
  entities: { type: "keyword" },
} as const;

/** 모든 mem-* 인덱스에 붙는 매핑. 임베딩 없이 BM25(기본 similarity)만 쓴다. */
export function indexTemplate() {
  const analyzed = { analyzer: TEXT_ANALYZER, search_analyzer: SEARCH_ANALYZER };
  return {
    index_patterns: [`${INDEX_PREFIX}*`],
    priority: 100,
    template: {
      settings: {
        number_of_shards: 1,
        number_of_replicas: 0,
        analysis: {
          filter: { identifier_split: IDENTIFIER_SPLIT },
          analyzer: {
            [TEXT_ANALYZER]: { tokenizer: "standard", filter: ["identifier_split", "lowercase", "flatten_graph"] },
            [SEARCH_ANALYZER]: { tokenizer: "standard", filter: ["identifier_split", "lowercase"] },
          },
        },
      },
      mappings: {
        dynamic: "strict",
        properties: {
          team: { type: "keyword" },
          repo: { type: "keyword" },
          doc_type: { type: "keyword" },
          event_time: { type: "date" },
          source_path: { type: "keyword", fields: { text: { type: "text", ...analyzed } } },
          git_sha: { type: "keyword" },
          header_path: { type: "text", ...analyzed, fields: { keyword: { type: "keyword", ignore_above: 512 } } },
          chunk_index: { type: "integer" },
          text: { type: "text", ...analyzed },
          ingested_at: { type: "date" },
          ...DAEMON_FIELDS,
        },
      },
    },
  };
}

/** hot(hotDays) → 스냅샷으로 cold repository에 저장 → 인덱스 삭제 */
export function ismPolicy(hotDays: number, snapshotRepository: string) {
  return {
    policy: {
      description: `memory: hot ${hotDays}d → snapshot(${snapshotRepository}) → delete`,
      default_state: "hot",
      states: [
        {
          name: "hot",
          actions: [],
          transitions: [{ state_name: "cold", conditions: { min_index_age: `${hotDays}d` } }],
        },
        {
          name: "cold",
          actions: [{ snapshot: { repository: snapshotRepository, snapshot: "ism-cold" } }],
          transitions: [{ state_name: "delete" }],
        },
        { name: "delete", actions: [{ delete: {} }], transitions: [] },
      ],
      ism_template: [{ index_patterns: [`${INDEX_PREFIX}*`], priority: 100 }],
    },
  };
}
