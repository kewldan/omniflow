import { describe, expect, test } from "bun:test";

import { GET } from "./route";

const at = (query: string) =>
  GET(new Request(`https://panel.example/miniapp/redirect.html?${query}`));

describe("the previous bot's opener", () => {
  test("forwards an application link to /open in the fragment", () => {
    const link = "incy://import/https://sub.example/AbC";
    const response = at(`url=${encodeURIComponent(link)}&lang=ru`);
    expect(response.status).toBe(302);
    expect(response.headers.get("Location")).toBe(`/open#${encodeURIComponent(link)}`);
  });

  test("never forwards a web address or a script", () => {
    for (const link of ["https://evil.example/", "javascript:alert(1)", "data:text/html,x", ""]) {
      expect(at(`url=${encodeURIComponent(link)}`).headers.get("Location")).toBe("/open");
    }
  });
});
