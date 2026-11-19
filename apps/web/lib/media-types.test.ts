/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { describeMediaType, describeMediaTypes } from "./media-types";

describe("describeMediaType", () => {
  test.each([
    ["image/png", "PNG"],
    ["image/jpeg", "JPEG"],
    ["IMAGE/WEBP", "WebP"],
    ["application/pdf", "PDF"],
    ["text/plain; charset=utf-8", "TXT"],
    ["application/x-rar", "RAR"],
    ["image/avif", "AVIF"],
    ["", "?"],
  ])("%s → %s", (mediaType, name) => {
    expect(describeMediaType(mediaType)).toBe(name);
  });
});

describe("describeMediaTypes", () => {
  test("lists the support allowlist the way a customer reads it", () => {
    expect(
      describeMediaTypes([
        "image/png",
        "image/jpeg",
        "image/webp",
        "application/pdf",
        "text/plain",
      ]),
    ).toBe("PNG, JPEG, WebP, PDF, TXT");
  });

  test("does not repeat a name two types share", () => {
    expect(describeMediaTypes(["text/plain", "text/plain; charset=utf-8"])).toBe("TXT");
  });
});
