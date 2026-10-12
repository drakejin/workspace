export const DOC_TYPES = ["spec", "adr", "prd", "plan", "knowledge", "guide", "note"] as const;
export type DocType = (typeof DOC_TYPES)[number];

/** OpenSearch에 들어가는 청크 한 건. 필드는 발표 §12 문서 필드 표와 같다. */
export interface MemoryChunk {
  readonly id: string;
  readonly team: string;
  readonly repo: string | null;
  readonly doc_type: DocType;
  readonly event_time: string;
  readonly source_path: string;
  readonly git_sha: string | null;
  readonly header_path: string;
  readonly chunk_index: number;
  readonly text: string;
  readonly ingested_at: string;
  /** 원문 전체(frontmatter 포함) sha256 — 데몬의 변경 감지 기준 */
  readonly content_sha: string;
  /** 본문(frontmatter 제외) sha256 — 판정 쌍·증류 캐시 키. 메타만 바뀌면 그대로다 */
  readonly body_sha: string;
  /** 파일 stat — 데몬이 sha 를 다시 계산할지 정하는 1차 게이트 */
  readonly mtime_ms: number;
  readonly size: number;
  readonly title: string;
  /** frontmatter supersedes 선언 (knowledge 개정판 → 구판 경로) */
  readonly supersedes: string | null;
  /** 이 문서를 밀어낸 문서 경로. 판정·선언이 정하고 데몬이 매 사이클 수렴시킨다 */
  readonly superseded_by: string | null;
  /** 증류 산출 (knowledge-cache 에서 복원). 없으면 null */
  readonly summary: string | null;
  readonly entities: readonly string[] | null;
}

export interface TimeRange {
  readonly from?: string;
  readonly to?: string;
}
