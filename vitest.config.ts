import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["packages/*/test/**/*.test.ts", "app/*/test/**/*.test.ts", "test/e2e/**/*.test.ts"],
    coverage: {
      include: ["packages/*/src/**", "app/*/src/**"],
      exclude: ["**/main.ts", "**/cli.ts"],
    },
  },
});
