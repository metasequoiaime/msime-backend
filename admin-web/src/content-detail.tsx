import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { RefreshCw, Trash2, X } from "lucide-react";
import { actionSchema, errorMessage } from "./api";
import { SkinPreview } from "./skin-preview";
import { useAuth } from "./auth";

export type ContentSection = "skins" | "dictionaries" | "replies";
const schema = z.object({ id: z.string(), name: z.string(), description: z.string(), owner_id: z.string(), author: z.string(), created_at: z.string(), updated_at: z.string().optional(), revision: z.number().optional(), downloads: z.number().optional(), saves: z.number().optional(), rating_count: z.number(), rating_average: z.number(), content: z.unknown() });
const entriesSchema = z.object({ entries: z.array(z.object({ kind: z.string(), code: z.string(), word: z.string(), weight: z.number() })).default([]) });
const replySchema = z.object({ prompt: z.string() });
const labels = { skins: "皮肤", dictionaries: "词库", replies: "回复模板" };
const actions = { skins: "delete_skin", dictionaries: "delete_dictionary", replies: "delete_reply" };
const date = (value: string) => new Date(value).toLocaleString("zh-CN");
export function ContentDetail({ section, id, close }: { section: ContentSection; id: string; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const { api } = useAuth(); const client = useQueryClient(); const [search, setSearch] = useState("");
  const result = useQuery({ queryKey: ["admin", "content", section, id], queryFn: async ({ signal }) => {
    const data = schema.parse(await api(`${section}/${encodeURIComponent(id)}`, signal));
    return { ...data, entries: section === "dictionaries" ? entriesSchema.parse(data.content).entries : [], prompt: section === "replies" ? replySchema.parse(data.content).prompt : "" };
  } });
  const mutation = useMutation({ mutationFn: async () => actionSchema.parse(await api("actions", undefined, { action: actions[section], id })), onSuccess: async () => { await client.invalidateQueries({ queryKey: ["admin"], refetchType: "none" }); close(); void client.refetchQueries({ queryKey: ["admin"], type: "active" }); } });
  useEffect(() => { const dialog = ref.current; dialog?.showModal(); return () => dialog?.close(); }, []);
  const data = result.data; const filtered = data?.entries.filter(entry => `${entry.kind} ${entry.code} ${entry.word}`.toLowerCase().includes(search.trim().toLowerCase())) ?? [];
  return <dialog ref={ref} aria-labelledby="content-title" onCancel={event => { if (mutation.isPending) event.preventDefault(); else close(); }}>
    <div className="dialog-heading"><h2 id="content-title">{labels[section]}详情</h2><div className="actions"><button type="button" disabled={result.isFetching || mutation.isPending} onClick={() => void result.refetch()}><RefreshCw size={16} aria-hidden="true" />刷新</button><button type="button" disabled={mutation.isPending} onClick={close}><X size={16} aria-hidden="true" />关闭</button></div></div>
    {result.isPending && <p role="status">正在加载内容…</p>}
    {result.isError && <p className="notice error" role="alert">{errorMessage(result.error)}</p>}
    {mutation.isError && <p className="notice error" role="alert">{errorMessage(mutation.error)}</p>}
    {data && <>{section === "skins" && <SkinPreview content={data.content} name={data.name} />}<dl className="user-facts"><dt>名称</dt><dd>{data.name}</dd><dt>描述</dt><dd>{data.description || "未填写"}</dd><dt>内容 ID</dt><dd>{data.id}</dd><dt>发布者</dt><dd>{data.author || "未设置名称"} · {data.owner_id}</dd><dt>发布时间</dt><dd>{date(data.created_at)}</dd>{data.updated_at && <><dt>更新时间</dt><dd>{date(data.updated_at)} · 修订 {data.revision}</dd></>}<dt>使用与评分</dt><dd>{section === "skins" ? `下载用户 ${data.downloads}` : `收藏用户 ${data.saves}`} · {data.rating_count ? `${data.rating_average.toFixed(1)} / 5（${data.rating_count} 人评分）` : "暂无评分"}</dd></dl>
      {section === "dictionaries" ? <><label className="content-search">搜索词条<input type="search" maxLength={200} value={search} onChange={event => setSearch(event.target.value)} placeholder="输入编码、词语或类型" /></label><p className="muted small" role="status">匹配 {filtered.length} / 共 {data.entries.length} 条</p><div className="table-wrap"><table><thead><tr><th scope="col">类型</th><th scope="col">编码</th><th scope="col">词语</th><th scope="col">权重</th></tr></thead><tbody>{filtered.map(entry => <tr key={JSON.stringify([entry.kind, entry.code, entry.word])}><td>{entry.kind}</td><td>{entry.code}</td><td>{entry.word}</td><td>{entry.weight}</td></tr>)}{!filtered.length && <tr><td colSpan={4}>没有匹配词条。</td></tr>}</tbody></table></div></> : section === "skins" ? <details className="skin-source"><summary>查看原始设计数据</summary><pre className="content-preview">{JSON.stringify(data.content, null, 2)}</pre></details> : <><h3>完整模板内容</h3><pre className="content-preview">{data.prompt}</pre></>}
      <div className="content-footer"><p className="muted small">删除会同时移除关联下载、收藏和评分记录，无法恢复。</p><button type="button" className="danger" disabled={mutation.isPending || result.isFetching || result.isError} onClick={() => { if (window.confirm(`永久删除${labels[section]}「${data.name}」及其关联记录？此操作无法恢复。`)) mutation.mutate(); }}><Trash2 size={16} aria-hidden="true" />{mutation.isPending ? "正在删除…" : `删除${labels[section]}`}</button></div>
    </>}
  </dialog>;
}
