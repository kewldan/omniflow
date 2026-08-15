import { redirect } from "next/navigation";

/**
 * The address earlier releases put in the bot's sign-in message.
 *
 * The issuer now hands out `/v1/account/auth/link`, which is the handler that
 * consumes the token. This page exists because a link already sitting in a chat
 * history still points here, and a magic link is a one-time credential: letting
 * an old one land on a 404 spends it for nothing and leaves the customer with no
 * way back. Forwarding costs one redirect and keeps every delivered link usable
 * for the ten minutes it was promised.
 *
 * The token is passed straight through rather than inspected. This page has no
 * business knowing whether it is valid — that is the redemption handler's
 * decision, and duplicating it here would be a second place that can disagree.
 */
export default async function MagicLinkForward({
  searchParams,
}: {
  searchParams: Promise<{ token?: string }>;
}) {
  const { token } = await searchParams;
  if (!token) {
    redirect("/account/sign-in?error=link_invalid");
  }
  redirect(`/v1/account/auth/link?token=${encodeURIComponent(token)}`);
}
