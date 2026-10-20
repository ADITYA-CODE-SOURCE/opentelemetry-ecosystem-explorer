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
import { describe, it, expect } from "vite-plus/test";
import { render, screen, fireEvent } from "@testing-library/react";
import { TruncatedDescription } from "./truncated-description";

const LONG = "Configure loggers. Wildcards * and ? are supported. Empty matches everything.";

describe("TruncatedDescription", () => {
  it("renders nothing when text is empty", () => {
    const { container } = render(<TruncatedDescription text="" />);
    expect(container.firstChild).toBeNull();
  });

  it("renders just the text and no toggle when text fits", () => {
    render(<TruncatedDescription text="Short description." />);
    expect(screen.getByText("Short description.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("renders summary plus a Show more button when text is long", () => {
    render(<TruncatedDescription text={LONG} />);
    expect(screen.getByText("Configure loggers.")).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Show more" });
    expect(button).toHaveAttribute("aria-expanded", "false");
    const rest = screen.getByTestId("truncated-rest");
    expect(rest).toBeInTheDocument();
    expect(rest).toHaveAttribute("hidden");
  });

  it("toggles aria-expanded and reveals/hides rest on click", () => {
    render(<TruncatedDescription text={LONG} />);
    const button = screen.getByRole("button", { name: "Show more" });
    fireEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(button).toHaveTextContent("Show less");
    expect(screen.getByTestId("truncated-rest")).not.toHaveAttribute("hidden");
    fireEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveTextContent("Show more");
    expect(screen.getByTestId("truncated-rest")).toHaveAttribute("hidden");
  });

  it("links button and rest via aria-controls", () => {
    render(<TruncatedDescription text={LONG} />);
    const button = screen.getByRole("button", { name: "Show more" });
    const rest = screen.getByTestId("truncated-rest");
    expect(button.getAttribute("aria-controls")).toBe(rest.id);
  });

  it("renders multi-paragraph descriptions as multiple <p> elements when expanded", () => {
    const text =
      "Configure the propagators. Entries are appended.\n\nThe value is a comma separated list. See details.\n\nBuilt-in identifiers include: tracecontext, baggage.";
    const { container } = render(<TruncatedDescription text={text} />);
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    const ps = container.querySelectorAll("p");
    expect(ps.length).toBeGreaterThanOrEqual(2);
  });

  it("renders bullet lists as <ul><li> when expanded", () => {
    const text =
      "Known values include the following supported entries.\n\n- alpha: first value\n- beta: second value";
    const { container } = render(<TruncatedDescription text={text} />);
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(container.querySelector("ul")).not.toBeNull();
    expect(container.querySelectorAll("ul li").length).toBe(2);
  });

  it("autolinks bare URLs in the rendered description", () => {
    const text =
      "Configure tracing. See the spec.\n\nReference: https://example.com/spec for full details.";
    render(<TruncatedDescription text={text} />);
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    const link = screen.getByRole("link", { name: "https://example.com/spec" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });
});
