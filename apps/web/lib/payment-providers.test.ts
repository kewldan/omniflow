import { describe, expect, test } from "bun:test";
import en from "../messages/en.json";
import ru from "../messages/ru.json";
import { PAYMENT_PROVIDERS, providerKey } from "./payment-providers";

describe("payment providers", () => {
  test("unknown slugs fall back to the generic label", () => {
    expect(providerKey("yookassa")).toBe("yookassa");
    expect(providerKey("not_a_provider")).toBe("unknown");
  });

  test("every provider has customer and operator copy in both languages", () => {
    for (const catalogue of [en, ru]) {
      const names = catalogue.account.commerce.checkout.provider.names as Record<string, string>;
      const adapters = catalogue.admin.paymentHealth.adapter as Record<string, string>;
      for (const provider of PAYMENT_PROVIDERS) {
        expect(names[provider]).toBeTruthy();
        expect(adapters[provider]).toBeTruthy();
      }
    }
  });
});
