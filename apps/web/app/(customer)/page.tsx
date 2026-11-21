import { redirect } from "next/navigation";

/**
 * The installation's front door goes straight to the account.
 *
 * A signed-in customer lands on the dashboard and a signed-out one on sign-in,
 * which the edge gate decides. Inside Telegram the Mini App's launch fragment
 * survives the redirect, so the sign-in screen still sees who opened it.
 */
export default async function CustomerHome({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  // The previous bot's cabinet links were `<site>/?ref=<code>`. They still
  // reach this page after a migration, so the code is carried to sign-in,
  // which holds it until the friend has an account.
  const { ref } = await searchParams;
  if (typeof ref === "string" && /^[A-Za-z0-9_-]{3,32}$/.test(ref)) {
    redirect(`/account/sign-in?ref=${encodeURIComponent(ref)}`);
  }
  redirect("/account");
}
