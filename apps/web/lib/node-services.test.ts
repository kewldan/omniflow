import { describe, expect, test } from "bun:test";

import { summarizeServices } from "./node-services";

describe("summarizeServices", () => {
  test("keeps display order and leaves unchecked services out", () => {
    const summary = summarizeServices([
      { services: { reddit: { state: "no" }, gemini: { region: "FIN", state: "yes" } } },
    ]);
    expect(summary).toEqual([
      { region: "FIN", service: "gemini", state: "yes" },
      { region: undefined, service: "reddit", state: "no" },
    ]);
  });

  test("servers that disagree read as some", () => {
    const summary = summarizeServices([
      {
        services: {
          gemini: { region: "RUS", state: "no" },
          youtube: { region: "RU", state: "limited" },
        },
      },
      {
        services: {
          gemini: { region: "POL", state: "yes" },
          youtube: { region: "RU", state: "limited" },
        },
      },
      {},
    ]);
    expect(summary).toEqual([
      { region: undefined, service: "gemini", state: "some" },
      { region: "RU", service: "youtube", state: "limited" },
    ]);
  });

  test("no checks, no badges", () => {
    expect(summarizeServices([{}, { services: {} }])).toEqual([]);
  });
});
