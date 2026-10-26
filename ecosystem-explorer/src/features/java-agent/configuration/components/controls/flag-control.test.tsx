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
import { describe, it, expect, vi, beforeEach } from "vite-plus/test";
import { render, screen, fireEvent } from "@testing-library/react";
import { FlagControl } from "./flag-control";
import type { FlagNode } from "@/types/configuration";

const validateField = vi.fn();
let mockValidationErrors: Record<string, string> = {};

vi.mock("@/hooks/use-configuration-builder", () => ({
  useConfigurationBuilder: () => ({
    state: {
      values: {},
      enabledSections: {},
      validationErrors: mockValidationErrors,
      version: "1.0.0",
      isDirty: false,
    },
    validateField,
    setValue: vi.fn(),
  }),
}));

const node: FlagNode = {
  controlType: "flag",
  key: "console",
  label: "Console",
  path: "exporter.console",
  nullable: true,
};

describe("FlagControl", () => {
  beforeEach(() => {
    validateField.mockReset();
    mockValidationErrors = {};
  });

  it("renders an off switch when value is null", () => {
    render(<FlagControl node={node} path={node.path} value={null} onChange={vi.fn()} />);
    const sw = screen.getByRole("switch");
    expect(sw).toHaveAttribute("aria-checked", "false");
  });

  it("renders an on switch when value is {}", () => {
    render(<FlagControl node={node} path={node.path} value={{}} onChange={vi.fn()} />);
    const sw = screen.getByRole("switch");
    expect(sw).toHaveAttribute("aria-checked", "true");
  });

  it("toggles value from null to {} on click", () => {
    const onChange = vi.fn();
    render(<FlagControl node={node} path={node.path} value={null} onChange={onChange} />);
    fireEvent.click(screen.getByRole("switch"));
    expect(onChange).toHaveBeenCalledWith("exporter.console", {});
  });

  it("toggles value from {} to null on click", () => {
    const onChange = vi.fn();
    render(<FlagControl node={node} path={node.path} value={{}} onChange={onChange} />);
    fireEvent.click(screen.getByRole("switch"));
    expect(onChange).toHaveBeenCalledWith("exporter.console", null);
  });

  it("renders the error from state when validationErrors has this path", () => {
    mockValidationErrors = { [node.path]: "Required" };
    render(<FlagControl node={node} path={node.path} value={null} onChange={vi.fn()} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Required");
  });
});
