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
import * as matchers from "@testing-library/jest-dom/matchers";
import type { TestingLibraryMatchers } from "@testing-library/jest-dom/matchers";
import { expect } from "vite-plus/test";

// jest-dom 7.0.1 still augments the pre-Vitest-5 Assertion<T> interface.
// Register its matchers directly and preserve Vitest's sync/async return type.
// Oxlint reports this on the closing `{}` line, so a next-line directive does not reach it.
// oxlint-disable typescript/no-empty-object-type -- Module augmentation adds the DOM matchers.
declare module "vitest" {
  interface Matchers<
    R extends void | Promise<void> = void | Promise<void>,
  > extends TestingLibraryMatchers<never, R> {}
}
// oxlint-enable typescript/no-empty-object-type

expect.extend(matchers);
