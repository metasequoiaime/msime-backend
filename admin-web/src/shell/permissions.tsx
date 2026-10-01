import { createContext, useContext, useMemo } from "react";
import type { ReactNode } from "react";
import type { Permission } from "../api/shell";
import { roleLabel } from "../api/shell";
import { useShell } from "./shell-data";

export type Permissions = {
  // can is false until /api/shell has loaded, so write buttons start disabled rather than flashing enabled.
  can: (permission: Permission) => boolean;
  permissions: ReadonlySet<string>;
  role: string;
  roleLabel: string;
  loaded: boolean;
};

const PermissionsContext = createContext<Permissions | null>(null);

// Every role can read every page; permissions only gate write actions, so pages disable buttons with can(key) and the server still enforces requirePerm.
export function PermissionsProvider({ children }: { children: ReactNode }) {
  const shell = useShell();
  const me = shell.data?.me;
  const value = useMemo<Permissions>(() => {
    const permissions = new Set(me?.permissions ?? []);
    return { can: permission => permissions.has(permission), permissions, role: me?.role ?? "", roleLabel: me ? roleLabel(me.role) : "", loaded: Boolean(me) };
  }, [me]);
  return <PermissionsContext.Provider value={value}>{children}</PermissionsContext.Provider>;
}

export function usePermissions(): Permissions {
  const value = useContext(PermissionsContext);
  if (!value) throw new Error("PermissionsProvider required");
  return value;
}

export const noPermissionHint = "当前角色没有此操作的权限";

// navAllowed reports whether a nav item is visible; items stay visible until /api/shell has loaded so the sidebar does not flicker for the common case.
export function navAllowed(permissions: Permissions, item: { permission?: Permission }): boolean {
  return !item.permission || !permissions.loaded || permissions.can(item.permission);
}
