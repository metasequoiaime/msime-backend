import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Save, Trash2 } from "lucide-react";
import { z } from "zod";
import { errorMessage } from "./api";
import { useAuth } from "./auth";
import { PageHeader } from "./shell";

const schema = z.object({ lanzou_url: z.string(), updated_at: z.string(), updated_by: z.string() });
// Mirrors the server rule so obvious mistakes are caught before a request; the server stays authoritative.
const lanzouURL = z.string().trim().max(512, "链接不能超过 512 字节。").refine(value => {
  if (value === "") return true;
  if (!value.startsWith("https://") || new TextEncoder().encode(value).length > 512) return false;
  try { const url = new URL(value); return url.protocol === "https:" && url.hostname !== "" && url.username === "" && url.password === ""; } catch { return false; }
}, "请输入以 https:// 开头的完整链接，不能包含账号密码。");

export function SiteSettings() {
  const { api } = useAuth(); const client = useQueryClient();
  const [draft, setDraft] = useState<string | null>(null);
  const [invalid, setInvalid] = useState("");
  const query = useQuery({ queryKey: ["admin", "site-settings"], queryFn: async ({ signal }) => schema.parse(await api("site-settings", signal)) });
  const mutation = useMutation({ mutationFn: async (value: string) => schema.parse(await api("site-settings", undefined, { lanzou_url: value })), onSuccess: async data => { client.setQueryData(["admin", "site-settings"], data); setDraft(null); await client.invalidateQueries({ queryKey: ["admin", "audit"] }); } });
  const current = query.data?.lanzou_url ?? "";
  const value = draft ?? current;
  const save = (next: string) => {
    const parsed = lanzouURL.safeParse(next);
    if (!parsed.success) { setInvalid(parsed.error.issues[0]?.message ?? "链接无效。"); return; }
    setInvalid("");
    if (parsed.data === "" && !window.confirm("清空蓝奏云盘链接？官网下载页将不再显示该入口。")) return;
    mutation.mutate(parsed.data);
  };
  return <><PageHeader page="site-settings" busy={query.isFetching} refresh={() => void query.refetch()} />
    {query.isError && <p className="notice error" role="alert">{errorMessage(query.error)}</p>}
    {mutation.isError && <p className="notice error" role="alert">{errorMessage(mutation.error)}</p>}
    {mutation.isSuccess && <p className="notice" role="status">{mutation.data.lanzou_url ? "链接已保存" : "链接已清空"}，已记录审计日志。官网有缓存，约 10 分钟内生效。</p>}
    <section className="panel"><h2>Windows 安装包 · 蓝奏云盘</h2><p className="muted small">官网下载页通过公开接口 <code>GET /v1/site/download-mirrors</code> 读取此链接，作为 Windows 安装包的国内下载入口。留空保存即隐藏该入口。</p>
      <dl className="user-facts"><dt>当前链接</dt><dd>{query.isPending ? "正在加载…" : current ? <a href={current} target="_blank" rel="noopener noreferrer">{current}</a> : "未设置"}</dd><dt>最近修改</dt><dd>{query.data?.updated_at ? `${new Date(query.data.updated_at).toLocaleString("zh-CN")} · ${query.data.updated_by}` : "从未修改"}</dd></dl>
      <form className="mt-4 flex flex-wrap items-center gap-2" onSubmit={event => { event.preventDefault(); save(value); }}><label className="sr-only" htmlFor="lanzou-url">蓝奏云盘分享链接</label><input id="lanzou-url" className="min-w-[240px] flex-1" type="text" inputMode="url" autoComplete="off" spellCheck={false} maxLength={512} value={value} onChange={event => { setDraft(event.target.value); setInvalid(""); }} placeholder="https://www.lanzouq.com/xxxxxx" aria-invalid={invalid !== ""} aria-describedby={invalid ? "lanzou-url-error" : undefined} /><button type="submit" disabled={mutation.isPending || !query.data || value.trim() === current}><span className="inline-flex items-center gap-1.5"><Save size={16} aria-hidden="true" />{mutation.isPending ? "正在保存…" : "保存"}</span></button><button type="button" className="danger" disabled={mutation.isPending || !query.data || !current} onClick={() => save("")}><span className="inline-flex items-center gap-1.5"><Trash2 size={16} aria-hidden="true" />清空</span></button></form>
      {invalid && <p id="lanzou-url-error" className="notice error" role="alert">{invalid}</p>}
    </section></>;
}
