import { useSyncExternalStore } from "react";

export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(listener => {
    const list = window.matchMedia(query);
    list.addEventListener("change", listener);
    return () => list.removeEventListener("change", listener);
  }, () => window.matchMedia(query).matches);
}

// Breakpoints from the design: mobile overlay nav below 760px, header search from 900px.
export const MOBILE_QUERY = "(max-width: 759px)";
