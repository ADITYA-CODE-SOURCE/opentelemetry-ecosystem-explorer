/*
 * Copyright The OpenTelemetry Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      https://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
// Type-only import: CI jobs that install only ecosystem-explorer/ still load this file when
// vp fmt searches upward, and the root vite-plus package is not installed there.
import type { ViteUserConfig } from "vite-plus";

export default {
  fmt: {
    semi: true,
    singleQuote: false,
    tabWidth: 2,
    trailingComma: "es5",
    printWidth: 100,
    arrowParens: "always",
    proseWrap: "always",
    sortPackageJson: false,
    sortTailwindcss: {},
    ignorePatterns: [
      "node_modules",
      ".venv",
      "dist",
      "build",
      ".vite",
      ".pytest_cache",
      "*.log",
      "package-lock.json",
      "pnpm-lock.yaml",
      "yarn.lock",
      "uv.lock",
      "bun.lock",
      ".vscode",
      ".idea",
      "coverage",
      "*.py",
      // Prettier never formatted TOML; keep pyproject.toml files out of scope.
      "*.toml",
      "ecosystem-explorer/public/data",
      "ecosystem-registry",
      "**/.repo",
      "**/.claude",
      ".ai",
    ],
  },
} satisfies ViteUserConfig;
