import { getTranslations } from "next-intl/server";

import { GuestCheckout } from "./guest-checkout";

export async function generateMetadata() {
  const translate = await getTranslations("public.guest");
  return { title: translate("title") };
}

/**
 * Buying a subscription without an account: an e-mail address, a plan, and a
 * payment method. The activation code arrives by e-mail once the payment
 * settles, and is redeemed after signing in.
 */
export default async function GuestPage({
  searchParams,
}: {
  searchParams: Promise<{ purchase?: string }>;
}) {
  const { purchase } = await searchParams;
  return (
    <main className="mx-auto flex w-full max-w-md flex-col gap-4 px-4 py-12">
      <GuestCheckout purchaseId={purchase ?? ""} />
    </main>
  );
}
