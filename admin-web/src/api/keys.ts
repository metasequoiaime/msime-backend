// Query key factory. Every admin query key starts with "admin" so logout (queryClient.clear) and broad refreshes (invalidateQueries({queryKey: keys.all})) cover them; each page unit adds keys under its own page segment through keys.page.
export const keys = {
  all: ["admin"] as const,
  session: ["admin", "session"] as const,
  shell: ["admin", "shell"] as const,
  search: (q: string) => ["admin", "search", q] as const,
  notifications: ["admin", "notifications"] as const,
  page: (page: string, ...parts: readonly unknown[]) => ["admin", page, ...parts] as const,
};
