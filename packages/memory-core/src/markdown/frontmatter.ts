import { parse } from "yaml";

export interface FrontmatterResult {
  readonly data: Readonly<Record<string, unknown>>;
  readonly body: string;
  /** body가 원문에서 시작하는 줄 번호(0부터) */
  readonly bodyLineOffset: number;
}

const FENCE = /^---\s*$/;

/** `---`로 감싼 YAML frontmatter를 떼어 낸다. 깨진 YAML이면 frontmatter가 없는 것으로 본다. */
export function splitFrontmatter(source: string): FrontmatterResult {
  const lines = source.split(/\r?\n/);
  if (lines.length === 0 || !FENCE.test(lines[0] ?? "")) {
    return { data: {}, body: source, bodyLineOffset: 0 };
  }
  const end = lines.findIndex((line, i) => i > 0 && FENCE.test(line));
  if (end === -1) return { data: {}, body: source, bodyLineOffset: 0 };

  const yamlText = lines.slice(1, end).join("\n");
  let data: unknown;
  try {
    data = parse(yamlText, { schema: "core" }) ?? {};
  } catch {
    return { data: {}, body: source, bodyLineOffset: 0 };
  }
  const isRecord = typeof data === "object" && data !== null && !Array.isArray(data);
  return {
    data: isRecord ? (data as Record<string, unknown>) : {},
    body: lines.slice(end + 1).join("\n"),
    bodyLineOffset: end + 1,
  };
}
