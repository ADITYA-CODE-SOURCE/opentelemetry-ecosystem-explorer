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
import { render, screen } from "@testing-library/react";
import { MarkdownDescription } from "./markdown-description";

describe("MarkdownDescription", () => {
  it("renders nothing when text is empty", () => {
    const { container } = render(<MarkdownDescription text="" />);
    expect(container.firstChild).toBeNull();
  });

  it("renders nothing when text is only whitespace", () => {
    const { container } = render(<MarkdownDescription text={"   \n  "} />);
    expect(container.firstChild).toBeNull();
  });

  it("renders a single paragraph as one <p>", () => {
    const { container } = render(<MarkdownDescription text="Hello world." />);
    const ps = container.querySelectorAll("p");
    expect(ps.length).toBe(1);
    expect(ps[0]).toHaveTextContent("Hello world.");
  });

  it("renders blank-line-separated paragraphs as multiple <p>", () => {
    const { container } = render(
      <MarkdownDescription text={"First paragraph.\n\nSecond paragraph."} />
    );
    const ps = container.querySelectorAll("p");
    expect(ps.length).toBe(2);
    expect(ps[0]).toHaveTextContent("First paragraph.");
    expect(ps[1]).toHaveTextContent("Second paragraph.");
  });

  it("renders a single-newline break inside a paragraph as a soft break", () => {
    const { container } = render(<MarkdownDescription text={"Line one.\nLine two."} />);
    const ps = container.querySelectorAll("p");
    expect(ps.length).toBe(1);
    expect(ps[0]).toHaveTextContent("Line one.");
    expect(ps[0]).toHaveTextContent("Line two.");
  });

  it("renders dash bullets as a <ul> with <li> children", () => {
    const { container } = render(<MarkdownDescription text={"- first\n- second\n- third"} />);
    const ul = container.querySelector("ul");
    expect(ul).not.toBeNull();
    const items = ul!.querySelectorAll("li");
    expect(items.length).toBe(3);
    expect(items[0]).toHaveTextContent("first");
    expect(items[2]).toHaveTextContent("third");
  });

  it("renders asterisk bullets as a <ul> with <li> children", () => {
    const { container } = render(<MarkdownDescription text={"* one\n* two"} />);
    const items = container.querySelectorAll("ul li");
    expect(items.length).toBe(2);
  });

  it("autolinks bare http URLs with safe rel and target", () => {
    render(<MarkdownDescription text="See https://example.com/spec for details." />);
    const link = screen.getByRole("link", { name: "https://example.com/spec" });
    expect(link).toHaveAttribute("href", "https://example.com/spec");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("does not render raw HTML tags as elements", () => {
    const { container } = render(
      <MarkdownDescription text={"Hello <script>alert(1)</script> world"} />
    );
    expect(container.querySelector("script")).toBeNull();
  });

  it("strips disallowed markdown elements (headings, tables, blockquotes)", () => {
    const { container } = render(
      <MarkdownDescription text={"# Heading\n\n> quote\n\n| a | b |\n|---|---|\n| 1 | 2 |"} />
    );
    expect(container.querySelector("h1")).toBeNull();
    expect(container.querySelector("blockquote")).toBeNull();
    expect(container.querySelector("table")).toBeNull();
  });

  it("renders inline code, strong, and em when present", () => {
    const { container } = render(
      <MarkdownDescription text={"a **bold** and _italic_ and `code` token"} />
    );
    expect(container.querySelector("strong")).not.toBeNull();
    expect(container.querySelector("em")).not.toBeNull();
    expect(container.querySelector("code")).not.toBeNull();
  });

  it("applies the supplied className to the wrapper element", () => {
    const { container } = render(<MarkdownDescription text="Hello." className="custom-cls" />);
    const wrapper = container.firstElementChild as HTMLElement;
    expect(wrapper.className).toContain("custom-cls");
  });
});
