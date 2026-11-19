import { getTranslations } from "next-intl/server";

import { UnsubscribeForm } from "./unsubscribe-form";

export async function generateMetadata() {
  const translate = await getTranslations("public.unsubscribe");
  return { robots: { index: false }, title: translate("title") };
}

/**
 * The page the unsubscribe link in a marketing e-mail opens.
 *
 * It asks for one click rather than unsubscribing on arrival, because mail
 * scanners follow every link in a message; the mail client's own one-click
 * button posts to the API directly and never sees this page.
 */
export default async function UnsubscribePage({
  searchParams,
}: {
  searchParams: Promise<{ token?: string }>;
}) {
  const { token } = await searchParams;
  return (
    <main className="mx-auto flex w-full max-w-md flex-col gap-4 px-4 py-16">
      <UnsubscribeForm token={token ?? ""} />
    </main>
  );
}
