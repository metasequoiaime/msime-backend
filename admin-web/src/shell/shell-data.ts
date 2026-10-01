import { useQuery } from "@tanstack/react-query";
import { useAPI } from "../api/client";
import { keys } from "../api/keys";
import { shellSchema } from "../api/shell";

// useShell polls /api/shell every 60s; pages read pending counts and the current admin from it instead of fetching again.
export function useShell() {
  const api = useAPI();
  return useQuery({
    queryKey: keys.shell,
    queryFn: ({ signal }) => api.get("shell", shellSchema, { signal }),
    refetchInterval: 60_000,
    staleTime: 30_000,
  });
}
