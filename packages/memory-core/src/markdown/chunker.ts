export interface MarkdownChunk {
  readonly headerPath: string;
  readonly text: string;
}

export interface ChunkOptions {
  /** 헤더가 없는 문서 머리말에 쓸 이름 (보통 파일 이름) */
  readonly rootTitle: string;
  /** 이 길이를 넘는 섹션은 문단 경계에서 나눈다. 코드 블록 안에서는 자르지 않는다. */
  readonly maxChars?: number;
}

const HEADER = /^(#{1,6})\s+(.+?)\s*#*\s*$/;
const FENCE_OPEN = /^\s{0,3}(`{3,}|~{3,})/;
const DEFAULT_MAX_CHARS = 4000;
const HEADER_SEPARATOR = " > ";

interface Section {
  readonly path: readonly string[];
  readonly lines: readonly string[];
}

interface Heading {
  readonly level: number;
  readonly title: string;
}

/** 마크다운을 헤더 계층 단위로 자른다. 코드 블록 안의 `#` 줄은 헤더로 보지 않는다. */
export function chunkMarkdown(body: string, options: ChunkOptions): MarkdownChunk[] {
  const sections = splitSections(body);
  const maxChars = options.maxChars ?? DEFAULT_MAX_CHARS;
  return sections.flatMap((section) => {
    const text = section.lines.join("\n").trim();
    if (text.length === 0) return [];
    const headerPath = (section.path.length > 0 ? section.path : [options.rootTitle]).join(HEADER_SEPARATOR);
    return splitLongText(text, maxChars).map((part) => ({ headerPath, text: part }));
  });
}

function splitSections(body: string): Section[] {
  const sections: Section[] = [];
  let stack: readonly Heading[] = [];
  let current: string[] = [];
  let fence: string | null = null;

  const flush = () => {
    sections.push({ path: stack.map((h) => h.title), lines: current });
    current = [];
  };

  for (const line of body.split(/\r?\n/)) {
    const fenceMatch = FENCE_OPEN.exec(line);
    if (fenceMatch) {
      const marker = fenceMatch[1] ?? "";
      if (fence === null) fence = marker;
      else if (marker.startsWith(fence[0] ?? "") && marker.length >= fence.length) fence = null;
      current.push(line);
      continue;
    }
    const header = fence === null ? HEADER.exec(line) : null;
    if (header) {
      flush();
      const level = (header[1] ?? "#").length;
      stack = [...stack.filter((h) => h.level < level), { level, title: header[2] ?? "" }];
      current.push(line);
      continue;
    }
    current.push(line);
  }
  flush();
  return sections;
}

/** 긴 섹션을 문단(빈 줄) 경계에서 나눈다. 코드 블록 안의 빈 줄은 경계로 쓰지 않는다. */
function splitLongText(text: string, maxChars: number): string[] {
  if (text.length <= maxChars) return [text];
  const blocks = paragraphBlocks(text);
  const parts: string[] = [];
  let buffer = "";
  for (const block of blocks) {
    const candidate = buffer.length === 0 ? block : `${buffer}\n\n${block}`;
    if (candidate.length > maxChars && buffer.length > 0) {
      parts.push(buffer);
      buffer = block;
    } else {
      buffer = candidate;
    }
  }
  if (buffer.length > 0) parts.push(buffer);
  return parts;
}

function paragraphBlocks(text: string): string[] {
  const blocks: string[] = [];
  let current: string[] = [];
  let inFence = false;
  for (const line of text.split("\n")) {
    if (FENCE_OPEN.test(line)) inFence = !inFence;
    if (!inFence && line.trim() === "") {
      if (current.length > 0) blocks.push(current.join("\n"));
      current = [];
      continue;
    }
    current.push(line);
  }
  if (current.length > 0) blocks.push(current.join("\n"));
  return blocks;
}
