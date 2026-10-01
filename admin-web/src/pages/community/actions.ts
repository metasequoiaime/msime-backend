import { useCallback } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { errorMessage, isAPIError, useAPI } from "../../api/client";
import { keys } from "../../api/keys";
import type { Moderation, Section } from "../../api/community";
import { removeReasons } from "../../api/community";
import { useConfirm } from "../../ui/confirm";
import { useToast } from "../../ui/toast";

// A moderation conflict means another reviewer changed the item's state or the author edited it since it was loaded, which reads better than the client's generic conflict copy.
function failure(error: unknown): string {
  return isAPIError(error, "conflict") ? "该内容刚被其他审核员处理或被作者修改，未做改动，请查看最新内容后重试。" : errorMessage(error);
}

// Target is what an action needs to know about an item, from a list row or a detail. updated_at is the version the moderator is looking at (only editable sections have one) and previous_moderation is a removed item's restore state. created_at tells the row apart from one the author published under the same id after deleting it.
export type Target = { section: Section; id: string; name: string; moderation: Moderation; moderation_reason?: string | null; previous_moderation?: Moderation | null; created_at?: string; updated_at?: string | null };

// approveValue pins an approval to the state, row and version the moderator saw, so a stale card cannot republish a removed item or publish content nobody reviewed.
function approveValue(target: Target) {
  return { from: target.moderation, ...(target.created_at ? { created_at: target.created_at } : {}), ...(target.updated_at ? { updated_at: target.updated_at } : {}) };
}

// useModeration wraps approve_content, remove_content and restore_content with the confirm dialog, toasts and the 撤销 that calls the reverse action.
export function useModeration() {
  const api = useAPI();
  const client = useQueryClient();
  const confirm = useConfirm();
  const toast = useToast();

  const refresh = useCallback(() => Promise.all([
    client.invalidateQueries({ queryKey: keys.page("community") }),
    client.invalidateQueries({ queryKey: keys.shell }),
  ]), [client]);

  const run = useCallback(async (body: Parameters<typeof api.action>[0]) => {
    // Failures carry the moderation-specific text, which both the action toasts and the toast's 撤销 show.
    try {
      await api.action(body);
    } catch (error) {
      throw new Error(failure(error));
    } finally {
      await refresh();
    }
  }, [api, refresh]);

  // undoTo puts one item back into the state it had before an action; a removed item gets its own restore state back, so a later restore does what it would have done before.
  const undoTo = useCallback((target: Target) => {
    const { section, id } = target;
    if (target.moderation === "removed") {
      const reason = target.moderation_reason || "恢复下架状态";
      return target.previous_moderation && target.previous_moderation !== "removed"
        ? run({ action: "remove_content", section, id, reason, value: { previous: target.previous_moderation } })
        : run({ action: "remove_content", section, id, reason });
    }
    return run({ action: "restore_content", section, id, value: { to: target.moderation } });
  }, [run]);

  const approve = useCallback(async (target: Target) => {
    if (target.moderation === "approved") return;
    try {
      await run({ action: "approve_content", section: target.section, id: target.id, value: approveValue(target) });
    } catch (error) {
      toast(`操作失败：${errorMessage(error)}`);
      return;
    }
    toast({ text: `「${target.name}」已上架`, undo: () => undoTo(target) });
  }, [run, toast, undoTo]);

  const remove = useCallback(async (target: Target) => {
    const verb = target.moderation === "approved" ? "下架" : "驳回";
    const reason = await confirm({ title: `${verb}「${target.name}」？`, description: "内容会从公开列表隐藏（作者本人仍可见），可随时恢复；原因记入审计日志。", okLabel: verb, reasons: removeReasons });
    if (reason === null) return;
    try {
      await run({ action: "remove_content", section: target.section, id: target.id, reason });
    } catch (error) {
      toast(`操作失败：${errorMessage(error)}`);
      return;
    }
    toast({ text: `「${target.name}」已${verb}：${reason}`, undo: () => target.moderation === "removed" ? run({ action: "remove_content", section: target.section, id: target.id, reason: target.moderation_reason || reason }) : run({ action: "restore_content", section: target.section, id: target.id }) });
  }, [confirm, run, toast]);

  const restore = useCallback(async (target: Target) => {
    try {
      await run({ action: "restore_content", section: target.section, id: target.id });
    } catch (error) {
      toast(`操作失败：${errorMessage(error)}`);
      return;
    }
    toast({ text: `「${target.name}」已恢复`, undo: () => undoTo(target) });
  }, [run, toast, undoTo]);

  return { approve, remove, restore };
}
