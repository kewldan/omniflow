import { describe, expect, test } from "bun:test";

import { isMiniAppFragment, miniAppUser } from "./telegram-miniapp";

describe("isMiniAppFragment", () => {
  test("recognises Telegram's launch parameters", () => {
    expect(isMiniAppFragment("#tgWebAppData=query_id%3DAA&tgWebAppVersion=8.0")).toBe(true);
    expect(isMiniAppFragment("#tgWebAppPlatform=ios")).toBe(true);
  });

  test("ignores an ordinary fragment", () => {
    expect(isMiniAppFragment("")).toBe(false);
    expect(isMiniAppFragment("#section-2")).toBe(false);
  });
});

describe("miniAppUser", () => {
  const launch = (user: unknown) =>
    new URLSearchParams({ auth_date: "1", hash: "x", user: JSON.stringify(user) }).toString();

  test("names the customer and keeps a t.me picture", () => {
    expect(
      miniAppUser(
        launch({
          first_name: "Иван",
          last_name: "Петров",
          photo_url: "https://t.me/i/userpic/320/a.jpg",
        }),
      ),
    ).toEqual({ name: "Иван Петров", photoUrl: "https://t.me/i/userpic/320/a.jpg" });
  });

  test("falls back to the username, and drops a picture from elsewhere", () => {
    expect(
      miniAppUser(launch({ username: "ivan", photo_url: "https://evil.example/a.jpg" })),
    ).toEqual({
      name: "@ivan",
      photoUrl: undefined,
    });
  });

  test("answers null without a usable user", () => {
    expect(miniAppUser("auth_date=1")).toBeNull();
    expect(miniAppUser("user=%7Bbroken")).toBeNull();
    expect(miniAppUser(launch({}))).toBeNull();
  });
});
