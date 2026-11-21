import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader } from "@omniflow/ui/card";
import Link from "next/link";
import { redirect } from "next/navigation";
import { getTranslations } from "next-intl/server";

import { CornerLocaleSwitch } from "@/components/intl/corner-locale-switch";
import { brandName } from "@/lib/metadata";

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
  const [translate, name] = await Promise.all([getTranslations("home"), brandName()]);
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl items-center px-6 py-20">
      <CornerLocaleSwitch />
      <Card className="max-w-2xl">
        <CardHeader>
          {/* The installation's own name, never the product's: this is the
              operator's front door, not Omniflow's. */}
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
            {translate("eyebrow", { name })}
          </p>
          <h1 className="font-semibold text-4xl leading-tight tracking-[-0.035em]">
            {translate("title")}
          </h1>
          <CardDescription className="text-base leading-7">
            {translate("description")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {/* The landing page is public, so it links into the account rather
              than assuming a session; the panel itself decides whether the
              visitor needs to sign in first. */}
          <Button asChild>
            <Link href="/account">{translate("action")}</Link>
          </Button>
        </CardContent>
      </Card>
    </main>
  );
}
