/// <reference types="bun" />
import { describe, expect, test } from "bun:test";

import { metricPolarity, noteDelta, relativeChange, trendNote } from "./metric-trends";

const flat = (value: number, days = 30) => Array.from({ length: days }, () => value);

describe("relativeChange", () => {
  test("is a fraction of the previous window", () => {
    expect(relativeChange(112, 100)).toBeCloseTo(0.12);
    expect(relativeChange(75, 100)).toBeCloseTo(-0.25);
  });

  test("has no percentage when the previous window was empty", () => {
    expect(relativeChange(5, 0)).toBeNull();
    expect(relativeChange(0, 0)).toBeNull();
  });
});

describe("trendNote for history", () => {
  test("a steady series says nothing", () => {
    expect(trendNote(flat(4), "history")).toBeNull();
  });

  test("an all-zero series says nothing", () => {
    expect(trendNote(flat(0), "history")).toBeNull();
  });

  test("the newest bucket above every other is a peak", () => {
    expect(trendNote([...flat(3, 29), 9], "history")).toEqual({ kind: "peak" });
  });

  test("a tie with an older bucket is not a peak", () => {
    expect(trendNote([9, ...flat(3, 28), 9], "history")?.kind).not.toBe("peak");
  });

  test("a recent week well above the usual pace", () => {
    const note = trendNote([...flat(2, 23), 4, 4, 4, 4, 4, 4, 3], "history");
    expect(note?.kind).toBe("above");
    expect(note && "shift" in note ? note.shift : 0).toBeCloseTo((27 / 7 - 2) / 2);
  });

  test("a recent week well below the usual pace", () => {
    expect(trendNote([...flat(4, 23), 1, 1, 1, 1, 1, 1, 1], "history")?.kind).toBe("below");
  });

  test("a small shift is noise", () => {
    expect(trendNote([...flat(10, 23), 11, 11, 11, 11, 11, 11, 10], "history")).toBeNull();
  });

  test("silence after activity is quiet", () => {
    expect(trendNote([...flat(2, 23), ...flat(0, 7)], "history")).toEqual({
      days: 7,
      kind: "quiet",
    });
  });

  test("too short a series to compare says nothing", () => {
    expect(trendNote([1, 2, 3], "history")).toBeNull();
  });
});

describe("trendNote for upcoming", () => {
  test("counts the next week and finds the busiest bucket", () => {
    const values = [1, 0, 2, 0, 0, 0, 1, 0, 5, ...flat(0, 21)];
    expect(trendNote(values, "upcoming")).toEqual({
      busiest: 5,
      busiestIndex: 8,
      days: 7,
      kind: "upcoming",
      soon: 4,
    });
  });

  test("nothing due says nothing", () => {
    expect(trendNote(flat(0), "upcoming")).toBeNull();
  });
});

test("an unknown metric is neutral", () => {
  expect(metricPolarity("paymentsFailed")).toBe("lower-is-better");
  expect(metricPolarity("somethingNew")).toBe("neutral");
});

test("notes carry the direction they describe", () => {
  expect(noteDelta({ kind: "peak" })).toBe(1);
  expect(noteDelta({ days: 7, kind: "quiet" })).toBe(-1);
});
