import { describe, expect, test } from "bun:test";

import { appLinkFromHash, isAppLink } from "./open-app";

describe("the /open page", () => {
  test("decodes an application link from the fragment", () => {
    const link = "happ://add/https://sub.example/api/sub/abc";
    expect(appLinkFromHash(`#${encodeURIComponent(link)}`)).toBe(link);
    // Go's url.QueryEscape writes a space as "+".
    expect(appLinkFromHash("#happ%3A%2F%2Fcrypt4%2FAB%2BCD")).toBe("happ://crypt4/AB+CD");
  });

  test("refuses anything that is not an application link", () => {
    for (const value of [
      "https://evil.example",
      "http://evil.example",
      "javascript://alert(1)",
      "data://text/html,x",
      "file:///etc/passwd",
      "not a link",
      "",
    ]) {
      expect(isAppLink(value)).toBe(false);
      expect(appLinkFromHash(`#${encodeURIComponent(value)}`)).toBe("");
    }
    expect(appLinkFromHash("#%E0%A4%A")).toBe("");
  });
});
