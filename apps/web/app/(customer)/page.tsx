import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { getTranslations } from "next-intl/server";

export default async function CustomerHome() {
  const translate = await getTranslations("home");
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl items-center px-6 py-20">
      <Card className="max-w-2xl">
        <CardHeader>
          <p className="font-mono text-[10px] text-subtle-foreground uppercase tracking-[0.16em]">
            {translate("eyebrow")}
          </p>
          <CardTitle className="text-4xl tracking-[-0.035em]">{translate("title")}</CardTitle>
          <CardDescription className="text-base leading-7">
            {translate("description")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Button>{translate("action")}</Button>
        </CardContent>
      </Card>
    </main>
  );
}
