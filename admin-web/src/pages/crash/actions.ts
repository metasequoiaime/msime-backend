import { useCallback, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { format } from "date-fns";
import { APIError, useAPI } from "../../api/client";
import type { API } from "../../api/client";
import { keys } from "../../api/keys";
import { crashIssueCreatedSchema, crashIssueErrors, crashStatusSchema } from "../../api/crash";
import type { CrashGroup, CrashStatus } from "../../api/crash";
import { useToast } from "../../ui/toast";

// createCrashIssue opens the group's GitHub issue, turning the endpoint's own error codes into Chinese messages for the failure toast.
function createCrashIssue(api: API, signature: string, keepalive: boolean) {
  return api.post(`crash-groups/${signature}/issue`, crashIssueCreatedSchema, {}, { keepalive }).catch((error: unknown) => {
    const message = error instanceof APIError ? crashIssueErrors[error.code] : undefined;
    throw message ? new Error(message) : error;
  });
}

export type CrashActions = {
  // overrides are optimistic statuses shown until the server confirms (or the toast's undo cancels) a change.
  overrides: Readonly<Record<string, CrashStatus>>;
  setStatus: (group: CrashGroup, to: CrashStatus, text: string) => Promise<void>;
  createIssue: (group: CrashGroup) => void;
};

// useCrashActions holds the page's two writes: a status change runs at once and its undo sends the reverse change; opening a GitHub issue cannot be reversed, so it is sent after the 4s undo window.
export function useCrashActions(): CrashActions {
  const api = useAPI();
  const client = useQueryClient();
  const toast = useToast();
  const [overrides, setOverrides] = useState<Record<string, CrashStatus>>({});
  const clear = useCallback((signature: string) => setOverrides(previous => {
    const next = { ...previous };
    delete next[signature];
    return next;
  }), []);
  const refresh = useCallback(() => client.invalidateQueries({ queryKey: keys.page("crash") }), [client]);

  const setStatus = useCallback(async (group: CrashGroup, to: CrashStatus, text: string) => {
    let from: CrashStatus = group.status;
    setOverrides(previous => ({ ...previous, [group.signature]: to }));
    try {
      const result = await api.action({ action: "crash_group_status", id: group.signature, value: to });
      // Undo restores what the server replaced, which differs from the shown status when another admin changed it meanwhile.
      const previous = crashStatusSchema.safeParse(result.previous);
      if (previous.success) from = previous.data;
      await refresh();
    } finally {
      clear(group.signature);
    }
    toast({ text, undo: () => api.action({ action: "crash_group_status", id: group.signature, value: from }).then(refresh) });
  }, [api, clear, refresh, toast]);

  const createIssue = useCallback((group: CrashGroup) => {
    setOverrides(previous => ({ ...previous, [group.signature]: "known" }));
    toast({
      text: "已创建 Issue 并标记为已知问题",
      delayCommit: ({ keepalive }) => createCrashIssue(api, group.signature, keepalive).then(refresh).finally(() => clear(group.signature)),
      undo: () => clear(group.signature),
      onCommitError: () => clear(group.signature),
    });
  }, [api, clear, refresh, toast]);

  return { overrides, setStatus, createIssue };
}

export function formatCount(value: number): string {
  return value.toLocaleString("zh-CN");
}

export function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : format(date, "yyyy-MM-dd HH:mm");
}
