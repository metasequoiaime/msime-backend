import { createContext, useContext, useMemo } from "react";
import type { ReactNode } from "react";
import type { Permission, Shell } from "../api/shell";
import { roleLabel } from "../api/shell";
import { useShell } from "./shell-data";

export type Permissions = {
  // can is false until /api/shell has loaded, so write buttons start disabled rather than flashing enabled.
  can: (permission: Permission) => boolean;
  permissions: ReadonlySet<string>;
  role: string;
  roleLabel: string;
  loaded: boolean;
  // feature 报告部署配置是否启用了某个可选功能；/api/shell 加载前一律为 false。
  feature: (key: keyof NonNullable<Shell["features"]>) => boolean;
};

const PermissionsContext = createContext<Permissions | null>(null);

// Every role can read every page; permissions only gate write actions, so pages disable buttons with can(key) and the server still enforces requirePerm.
export function PermissionsProvider({ children }: { children: ReactNode }) {
  const shell = useShell();
  const me = shell.data?.me;
  const features = shell.data?.features;
  const value = useMemo<Permissions>(() => {
    const permissions = new Set(me?.permissions ?? []);
    return { can: permission => permissions.has(permission), permissions, role: me?.role ?? "", roleLabel: me ? roleLabel(me.role) : "", loaded: Boolean(me), feature: key => features?.[key] ?? false };
  }, [me, features]);
  return <PermissionsContext.Provider value={value}>{children}</PermissionsContext.Provider>;
}

export function usePermissions(): Permissions {
  const value = useContext(PermissionsContext);
  if (!value) throw new Error("PermissionsProvider required");
  return value;
}

export const noPermissionHint = "当前角色没有此操作的权限";

// navAllowed 判断导航项是否可见：受权限限制的项在 /api/shell 加载前保持可见，避免常见情况下侧栏闪烁；依赖可选功能的项要等加载后确认功能已启用才出现，未配置的部署不会闪现它。
export function navAllowed(permissions: Permissions, item: { permission?: Permission; feature?: keyof NonNullable<Shell["features"]> }): boolean {
  if (item.feature && !permissions.feature(item.feature)) return false;
  return !item.permission || !permissions.loaded || permissions.can(item.permission);
}
