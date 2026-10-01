import { useEffect, useSyncExternalStore } from "react";

// Counts open modal overlays (confirm dialog, detail drawer) so page hotkeys stay inert while one is open, matching the prototype where the confirm dialog swallows every key but Enter and Escape.
let open = 0;
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

export function useRegisterOverlay(active: boolean) {
  useEffect(() => {
    if (!active) return;
    open++;
    emit();
    return () => { open--; emit(); };
  }, [active]);
}

export function overlayOpen(): boolean {
  return open > 0;
}

export function useOverlayOpen(): boolean {
  return useSyncExternalStore(listener => { listeners.add(listener); return () => listeners.delete(listener); }, overlayOpen);
}
