import type { ReactNode } from "react";

import { CustomerMeasurement } from "@/components/analytics/customer-measurement";
import { PagesTelegramBack } from "@/components/pages-telegram-back";

/**
 * The chrome for published documents.
 *
 * Deliberately spare, and deliberately outside both panels. A reader here is
 * either deciding whether to become a customer or reviewing the installation on
 * behalf of a payment provider, and neither of them wants a navigation sidebar
 * for an account they do not have.
 *
 * The documents are public pages a visitor arrives at from an advert as often
 * as the landing page, so the operator's measurement is mounted here too.
 * Opened from the Mini App, Telegram's back button leads back into the account.
 */
export default function PagesLayout({ children }: { children: ReactNode }) {
  return (
    <>
      <main className="mx-auto w-full max-w-2xl px-4 py-10 sm:px-6 sm:py-14">{children}</main>
      <CustomerMeasurement />
      <PagesTelegramBack />
    </>
  );
}
