/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { describeUserAgent, userAgentLabel } from "./user-agent";

const MAC_CHROME =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36";
const MAC_SAFARI =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Safari/605.1.15";
const IPHONE_SAFARI =
  "Mozilla/5.0 (iPhone; CPU iPhone OS 19_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Mobile/15E148 Safari/604.1";
const WINDOWS_EDGE =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36 Edg/156.0.0.0";
const ANDROID_YANDEX =
  "Mozilla/5.0 (Linux; Android 15; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 YaBrowser/26.1.0 Mobile Safari/537.36";
const LINUX_FIREFOX = "Mozilla/5.0 (X11; Linux x86_64; rv:146.0) Gecko/20100101 Firefox/146.0";

describe("userAgentLabel", () => {
  test.each([
    [MAC_CHROME, "Chrome · macOS"],
    [MAC_SAFARI, "Safari · macOS"],
    [IPHONE_SAFARI, "Safari · iPhone"],
    [WINDOWS_EDGE, "Edge · Windows"],
    [ANDROID_YANDEX, "Yandex Browser · Android"],
    [LINUX_FIREFOX, "Firefox · Linux"],
  ])("%s", (agent, label) => {
    expect(userAgentLabel(agent)).toBe(label);
  });

  test("a client it cannot read is null rather than a guess", () => {
    expect(userAgentLabel("")).toBeNull();
    expect(userAgentLabel(undefined)).toBeNull();
    expect(userAgentLabel("curl/8.9.1")).toBeNull();
  });

  test("half a description is still a description", () => {
    expect(userAgentLabel("SomeBot/1.0 (Windows NT 10.0)")).toBe("Windows");
  });
});

describe("describeUserAgent", () => {
  test("knows a phone from a desktop", () => {
    expect(describeUserAgent(IPHONE_SAFARI).mobile).toBe(true);
    expect(describeUserAgent(ANDROID_YANDEX).mobile).toBe(true);
    expect(describeUserAgent(MAC_CHROME).mobile).toBe(false);
  });
});
