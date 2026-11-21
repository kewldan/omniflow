"use client";

import { Badge } from "@omniflow/ui/badge";
import { Button } from "@omniflow/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@omniflow/ui/card";
import { Checkbox } from "@omniflow/ui/checkbox";
import { Input } from "@omniflow/ui/input";
import { Label } from "@omniflow/ui/label";
import { toast } from "@omniflow/ui/toast";
import { useTranslations } from "next-intl";
import { useId, useState } from "react";
import useSWR from "swr";
import { usePermissionLabel } from "@/components/admin/enum-label";
import { type ApiError, fetcher } from "@/lib/api";
import { useOperatorAction } from "@/lib/operations";
import { useSession } from "@/lib/session";
import type { RbacCatalog } from "./roles";

/** Never part of a custom role: managing operators stays with owners. */
const RESERVED = ["admins.write", "admins.roles"];

/**
 * Custom roles: a name for a set of permissions from the catalogue the API
 * enforces. Nobody defines a role stronger than their own — the API refuses a
 * permission its author does not hold — and deleting a role removes it from
 * everybody who held it.
 */
export function CustomRoles() {
  const translate = useTranslations("admin.customRoles");
  const permissionLabel = usePermissionLabel();
  const { can } = useSession();
  const { data: catalog, mutate } = useSWR<RbacCatalog, ApiError>(
    "/v1/panel/rbac/catalog",
    fetcher,
  );
  const { run, pending, error } = useOperatorAction();
  const ids = { key: useId(), name: useId() };
  const [key, setKey] = useState("");
  const [name, setName] = useState("");
  const [permissions, setPermissions] = useState<string[]>([]);
  const editable = can("admins.roles");
  const custom = (catalog?.roles ?? []).filter((entry) => entry.custom);
  const offered = (catalog?.permissions ?? []).filter(
    (permission) => !RESERVED.includes(permission),
  );

  async function save() {
    const role = key.startsWith("custom-") ? key : `custom-${key}`;
    if (
      await run(`/v1/panel/rbac/custom-roles/${role}`, {
        method: "PUT",
        body: { name, permissions },
      })
    ) {
      toast.success(translate("saved"));
      setKey("");
      setName("");
      setPermissions([]);
      await mutate();
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>{translate("title")}</CardTitle>
        <CardDescription>{translate("description")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {custom.length === 0 ? (
          <p className="text-muted-foreground text-sm">{translate("empty")}</p>
        ) : null}
        {custom.map((entry) => (
          <div
            className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-3"
            key={entry.role}
          >
            <div className="flex flex-col gap-1">
              <span className="font-medium">
                {entry.name}{" "}
                <span className="font-mono text-muted-foreground text-xs">{entry.role}</span>
              </span>
              <span className="flex flex-wrap gap-1">
                {entry.permissions.map((permission) => (
                  <Badge key={permission} variant="outline">
                    {permissionLabel(permission)}
                  </Badge>
                ))}
              </span>
            </div>
            {editable ? (
              <div className="flex gap-2">
                <Button
                  onClick={() => {
                    setKey(entry.role);
                    setName(entry.name ?? "");
                    setPermissions(entry.permissions);
                  }}
                  size="sm"
                  variant="outline"
                >
                  {translate("edit")}
                </Button>
                <Button
                  disabled={pending}
                  onClick={async () => {
                    if (
                      await run(`/v1/panel/rbac/custom-roles/${entry.role}`, { method: "DELETE" })
                    ) {
                      await mutate();
                    }
                  }}
                  size="sm"
                  variant="ghost"
                >
                  {translate("delete")}
                </Button>
              </div>
            ) : null}
          </div>
        ))}
        {editable ? (
          <div className="flex flex-col gap-3 border-t pt-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.key}>{translate("key")}</Label>
                <Input
                  id={ids.key}
                  onChange={(event) => setKey(event.target.value.toLowerCase())}
                  placeholder="custom-desk"
                  value={key}
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={ids.name}>{translate("name")}</Label>
                <Input
                  id={ids.name}
                  onChange={(event) => setName(event.target.value)}
                  value={name}
                />
              </div>
            </div>
            <div className="grid gap-2 sm:grid-cols-3">
              {offered.map((permission) => {
                const checkboxId = `${ids.key}-${permission}`;
                return (
                  <div className="flex items-center gap-2" key={permission}>
                    <Checkbox
                      checked={permissions.includes(permission)}
                      disabled={!can(permission)}
                      id={checkboxId}
                      onCheckedChange={(checked) =>
                        setPermissions(
                          checked
                            ? [...permissions, permission]
                            : permissions.filter((item) => item !== permission),
                        )
                      }
                    />
                    <Label htmlFor={checkboxId}>{permissionLabel(permission)}</Label>
                  </div>
                );
              })}
            </div>
            {error ? (
              <p className="text-destructive text-sm" role="alert">
                {error.message}
              </p>
            ) : null}
            <Button
              className="self-start"
              disabled={pending || !key || !name.trim() || permissions.length === 0}
              onClick={save}
            >
              {translate("save")}
            </Button>
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}
