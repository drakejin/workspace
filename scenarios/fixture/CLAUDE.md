# scenario-workspace — 에이전트 규칙

drakejin-workspace-memory-mcp 시나리오 테스트용 가상 meta-repository 다. 프로젝트 alpha-api, beta-web 과 결정은 모두 예시다.

1. **작업을 시작하면 `search_memory` 를 먼저 부른다** (MCP 서버 `drakejin-workspace-memory-mcp`). 파일 트리를 뒤지기 전에.
2. 날짜·기간·주제·프로젝트가 정해진 질문이면 그 필터(`date`, `time_range`, `topic`, `project`)를 쓴다. 쓸 수 있는 값은 `search_filters` 로 본다.
3. 결과의 `source_path` 를 Read 해서 원문을 근거로 답하고, 근거 문서 경로를 함께 적는다.
