import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Toast } from "radix-ui";
import { errorMessage } from "../api/client";

const TOAST_MS = 4000;

export type ToastOptions = {
  text: string;
  // undo shows 撤销. For a reversible action that already ran, undo calls the reverse action. Combined with delayCommit, undo only needs to restore local optimistic state because the request is never sent.
  undo?: () => unknown;
  // delayCommit defers an irreversible request (merging a PR) for 4s. 撤销 cancels it; the timer, a newer toast, or pagehide flushes it. On pagehide it is called with keepalive: true and must pass that to requestAPI so the request survives the unload.
  delayCommit?: (options: { keepalive: boolean }) => Promise<unknown>;
  // onCommitError runs when the delayed commit fails, after the failure toast is shown; use it to roll back optimistic state.
  onCommitError?: (error: unknown) => void;
};

export type ShowToast = (options: ToastOptions | string) => void;

type Current = ToastOptions & { id: number };
type PendingCommit = { id: number; run: (keepalive: boolean) => void; timer: number };

const ToastContext = createContext<ShowToast | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [current, setCurrent] = useState<Current | null>(null);
  const [open, setOpen] = useState(false);
  const pending = useRef<PendingCommit | null>(null);
  const hideTimer = useRef(0);
  const nextID = useRef(0);
  const shownID = useRef(0);

  const flush = useCallback((keepalive: boolean) => {
    const commit = pending.current;
    if (!commit) return;
    pending.current = null;
    window.clearTimeout(commit.timer);
    commit.run(keepalive);
  }, []);

  const show = useCallback<ShowToast>(input => {
    const options = typeof input === "string" ? { text: input } : input;
    // A newer toast replaces the old one; an unfinished delayed commit of the old toast is sent now rather than dropped.
    flush(false);
    const id = ++nextID.current;
    shownID.current = id;
    setCurrent({ ...options, id });
    setOpen(true);
    window.clearTimeout(hideTimer.current);
    hideTimer.current = window.setTimeout(() => setOpen(false), TOAST_MS);
    const { delayCommit } = options;
    if (delayCommit) {
      const run = (keepalive: boolean) => {
        delayCommit({ keepalive }).catch(error => {
          show(`操作失败：${errorMessage(error)}`);
          options.onCommitError?.(error);
        });
      };
      pending.current = { id, run, timer: window.setTimeout(() => flush(false), TOAST_MS) };
    }
  }, [flush]);

  useEffect(() => {
    const onPageHide = () => flush(true);
    window.addEventListener("pagehide", onPageHide);
    return () => {
      window.removeEventListener("pagehide", onPageHide);
      window.clearTimeout(hideTimer.current);
      flush(true);
    };
  }, [flush]);

  const undo = () => {
    if (!current) return;
    const commit = pending.current;
    if (commit && commit.id === current.id) {
      window.clearTimeout(commit.timer);
      pending.current = null;
    }
    const result = current.undo?.();
    if (result instanceof Promise) {
      result.then(() => show("已撤销"), error => show(`撤销失败：${errorMessage(error)}`));
    } else {
      show("已撤销");
    }
  };

  const canUndo = Boolean(current && (current.undo || current.delayCommit));
  return <ToastContext.Provider value={show}>
    <Toast.Provider duration={Number.POSITIVE_INFINITY} swipeDirection="down" label="通知">
      {children}
      {current && <Toast.Root key={current.id} open={open} type="foreground"
        // 撤销 closes its own toast after its click handler already showed 已撤销; ignore close events from a toast that has been replaced.
        onOpenChange={next => { if (!next && shownID.current === current.id) setOpen(false); }}
        className="fixed bottom-7 left-1/2 z-80 flex max-w-[calc(100vw-32px)] animate-toast-in items-center gap-3.5 rounded-xl bg-ink px-[18px] py-3 text-sm text-bg shadow-dialog">
        <Toast.Description>{current.text}</Toast.Description>
        {canUndo && <Toast.Action altText="撤销刚才的操作" onClick={undo} className="shrink-0 font-bold text-bg underline underline-offset-2">撤销</Toast.Action>}
      </Toast.Root>}
      <Toast.Viewport className="fixed bottom-0 left-0 z-80 m-0 list-none p-0 outline-none" />
    </Toast.Provider>
  </ToastContext.Provider>;
}

export function useToast(): ShowToast {
  const value = useContext(ToastContext);
  if (!value) throw new Error("ToastProvider required");
  return value;
}
