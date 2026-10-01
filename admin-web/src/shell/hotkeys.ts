import { useEffect, useRef } from "react";
import { useHotkeys } from "react-hotkeys-hook";
import type { PageKey } from "../nav";
import { overlayOpen } from "../ui/overlay";

export type HotkeyMap = Record<string, (event: KeyboardEvent) => void>;

// usePageHotkeys binds single-key page shortcuts (e.g. {j: next, k: previous, a: approve, r: reject}). They are ignored while typing in a form field, with Meta/Ctrl/Alt held, or while a confirm dialog or drawer is open. Keys use react-hotkeys-hook syntax; the map may change between renders.
export function usePageHotkeys(page: PageKey, map: HotkeyMap, options: { enabled?: boolean } = {}) {
  const handlers = useRef(map);
  useEffect(() => { handlers.current = map; });
  const keys = Object.keys(map).join(",");
  useHotkeys(keys, (event, hotkey) => {
    if (overlayOpen() || event.metaKey || event.ctrlKey || event.altKey) return;
    // react-hotkeys-hook reports the matched combination lowercased, so look the handler up case-insensitively ("Shift+A" in the map matches "shift+a").
    const handler = Object.entries(handlers.current).find(([key]) => key.trim().toLowerCase() === hotkey.hotkey)?.[1];
    if (!handler) return;
    event.preventDefault();
    handler(event);
  }, { enabled: (options.enabled ?? true) && keys.length > 0, description: page }, [keys, page]);
}
