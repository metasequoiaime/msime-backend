import { useCallback } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

export type PageSearch = Record<string, string | undefined>;

// validatePageSearch keeps every page's search params as plain strings; pages parse numbers themselves.
export function validatePageSearch(search: Record<string, unknown>): PageSearch {
  const result: PageSearch = {};
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined || value === null || value === "") continue;
    result[key] = typeof value === "string" ? value : String(value);
  }
  return result;
}

// usePageSearch returns the current page's search params, e.g. ?focus=<id> set by global search and notifications.
export function usePageSearch(): PageSearch {
  return validatePageSearch(useSearch({ strict: false }) as Record<string, unknown>);
}

// useSetPageSearch merges a patch into the current search params; undefined or "" removes a key. replace avoids a history entry per filter click.
export function useSetPageSearch() {
  const navigate = useNavigate();
  return useCallback((patch: PageSearch, options: { replace?: boolean } = {}) => {
    void navigate({
      to: ".",
      replace: options.replace ?? true,
      search: ((previous: Record<string, unknown>) => validatePageSearch({ ...previous, ...patch })) as never,
    });
  }, [navigate]);
}
