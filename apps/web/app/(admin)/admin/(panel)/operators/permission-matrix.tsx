"use client";

import { Badge } from "@omniflow/ui/badge";
import { Card } from "@omniflow/ui/card";
import { Skeleton } from "@omniflow/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@omniflow/ui/table";
import { useTranslations } from "next-intl";

import { StateNotice } from "@/components/admin/state-notice";

import { type RbacCatalog, ROLES } from "./roles";

/** The verbs a permission name can end in, in the order they are listed. */
const VERBS = ["read", "write", "export", "roles"] as const;

/**
 * What each built-in role may do, one row per area of the product.
 *
 * Read straight from the compiled-in catalogue the API enforces, so it cannot
 * describe a role differently from how the server treats it. Permission names
 * are grouped by their domain (`customers.read`, `customers.write` → one row)
 * because twenty-seven raw identifiers in six columns is a grid nobody reads.
 */
export function PermissionMatrix({
  catalog,
  error,
  loading,
}: {
  catalog: RbacCatalog | undefined;
  error: boolean;
  loading: boolean;
}) {
  const translate = useTranslations("admin.operators.matrix");
  const translateRole = useTranslations("admin.roles");

  if (loading) {
    return <Skeleton className="h-64 w-full" />;
  }
  if (error || !catalog) {
    return (
      <StateNotice
        description={translate("errorDescription")}
        title={translate("error")}
        variant="danger"
      />
    );
  }

  const domains: string[] = [];
  for (const permission of catalog.permissions) {
    const domain = permission.split(".")[0] ?? permission;
    if (!domains.includes(domain)) {
      domains.push(domain);
    }
  }
  const granted = new Map(catalog.roles.map((entry) => [entry.role, new Set(entry.permissions)]));

  return (
    <Card className="overflow-hidden">
      <div className="border-border border-b px-4 py-3">
        <h2 className="font-semibold text-base">{translate("title")}</h2>
        <p className="text-muted-foreground text-sm">{translate("description")}</p>
      </div>
      <div className="overflow-x-auto">
        <Table>
          <caption className="sr-only">{translate("title")}</caption>
          <TableHeader>
            <TableRow>
              <TableHead scope="col">{translate("area")}</TableHead>
              {ROLES.map((role) => (
                <TableHead key={role} scope="col">
                  {translateRole(role)}
                </TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {domains.map((domain) => (
              <TableRow key={domain}>
                <TableHead className="font-medium" scope="row">
                  {translate(`domains.${domain}`)}
                </TableHead>
                {ROLES.map((role) => {
                  const verbs = VERBS.filter((verb) => granted.get(role)?.has(`${domain}.${verb}`));
                  return (
                    <TableCell key={role}>
                      {verbs.length === 0 ? (
                        <span className="text-subtle-foreground text-xs">
                          <span aria-hidden>—</span>
                          <span className="sr-only">{translate("none")}</span>
                        </span>
                      ) : (
                        <div className="flex flex-wrap gap-1">
                          {verbs.map((verb) => (
                            <Badge key={verb} variant={verb === "read" ? "neutral" : "warning"}>
                              {translate(`verbs.${verb}`)}
                            </Badge>
                          ))}
                        </div>
                      )}
                    </TableCell>
                  );
                })}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </Card>
  );
}
