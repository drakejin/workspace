const TEAM_PATTERN = /^[a-z0-9][a-z0-9-]*$/;
const INDEX_PATTERN = /^mem-([a-z0-9][a-z0-9-]*)-(\d{4})\.(\d{2})$/;

export const INDEX_PREFIX = "mem-";

export function assertTeam(team: string): string {
  if (!TEAM_PATTERN.test(team)) {
    throw new Error(`team 이름이 잘못됐습니다: "${team}" (소문자, 숫자, 하이픈만)`);
  }
  return team;
}

/** 월별 인덱스 이름. mem-{team}-{yyyy.MM} */
export function monthlyIndex(team: string, eventTime: Date): string {
  const yyyy = eventTime.getUTCFullYear();
  const mm = String(eventTime.getUTCMonth() + 1).padStart(2, "0");
  return `${INDEX_PREFIX}${assertTeam(team)}-${yyyy}.${mm}`;
}

/** 팀의 hot 인덱스 전체를 가리키는 읽기 alias */
export function hotAlias(team: string): string {
  return `${INDEX_PREFIX}${assertTeam(team)}-hot`;
}

/**
 * 팀의 모든 월별 인덱스(hot + 복원된 cold)를 잡는 패턴.
 * 주의: mem-ads-* 는 mem-ads-data-* 도 잡는다. 결과는 parseIndexName(...).team 이나 team 필드로 다시 거른다.
 */
export function teamIndexPattern(team: string): string {
  return `${INDEX_PREFIX}${assertTeam(team)}-*`;
}

export interface ParsedIndexName {
  readonly team: string;
  readonly monthStart: Date;
  /** 그 달의 마지막 순간 다음(다음 달 1일 00:00 UTC) */
  readonly monthEnd: Date;
}

export function parseIndexName(index: string): ParsedIndexName | null {
  const m = INDEX_PATTERN.exec(index);
  if (!m) return null;
  const [, team, yyyy, mm] = m as unknown as [string, string, string, string];
  const year = Number(yyyy);
  const month = Number(mm) - 1;
  return {
    team,
    monthStart: new Date(Date.UTC(year, month, 1)),
    monthEnd: new Date(Date.UTC(year, month + 1, 1)),
  };
}
