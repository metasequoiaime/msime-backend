import { Download, RotateCcw, Search } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { actionSchema, errorMessage, listSchema } from "./api";
import type { Row } from "./api";
import { useAuth } from "./auth";
import { columns, actionLabels } from "./pages";
import type { ListPage } from "./pages";
import { ContentDetail } from "./content-detail";
import { UserDetail } from "./user-detail";
import { downloadCSV } from "./export";
import { PageHeader } from "./shell";

export function ResourceList({ section }: { section: ListPage }) {
  const { api } = useAuth(); const client = useQueryClient();
  const [page, setPage] = useState(1); const [search, setSearch] = useState(""); const [query, setQuery] = useState(""); const [detail, setDetail] = useState<Row | null>(null);
  const [platform, setPlatform] = useState(""); const [version, setVersion] = useState(""); const [status, setStatus] = useState(""); const [auditAction, setAuditAction] = useState(""); const [actor, setActor] = useState("");
  const [compact, setCompact] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [batchMessage, setBatchMessage] = useState("");
  const batch = useMutation({
    mutationFn: async (action: string) => {
      const failures: string[] = []; let completed = 0;
      for (const id of selected) {
        try { actionSchema.parse(await api("actions", undefined, { action, id })); completed++; }
        catch { failures.push(id); }
      }
      return { completed, failures };
    },
    onSuccess: async ({ completed, failures }) => {
      setSelected(new Set(failures));
      setBatchMessage(`已处理 ${completed} 条${failures.length ? `，${failures.length} 条失败，可重试或刷新检查记录。` : "，已记录审计日志。"}`);
      await client.invalidateQueries({ queryKey: ["admin"] });
    },
  });
  const selectionKey = JSON.stringify([page, query, platform, version, status, auditAction, actor]);
  useEffect(() => { void selectionKey; setSelected(new Set()); setBatchMessage(""); }, [selectionKey]);
  const events = section === "downloads" || section === "crashes";
  const filtered = Boolean(query || platform || version || status || auditAction || actor);
  const params = new URLSearchParams({ page: String(page), q: query, platform, version, status, action: auditAction, actor });
  const result = useQuery({ queryKey: ["admin", section, page, query, platform, version, status, auditAction, actor], queryFn: async ({ signal }) => listSchema.parse(await api(`${section}?${params}`, signal)) });
  const mutation = useMutation({
    mutationFn: async ({ action, id }: { action: string; id: string }) => actionSchema.parse(await api("actions", undefined, { action, id })),
    onSuccess: () => client.invalidateQueries({ queryKey: ["admin"] }),
  });
  useEffect(() => {
    if (result.data && page > 1 && !result.data.items.length) setPage(Math.max(1, Math.ceil(result.data.total / 50)));
  }, [result.data, page]);
  const reset = () => { setSearch(""); setQuery(""); setPlatform(""); setVersion(""); setStatus(""); setAuditAction(""); setActor(""); setPage(1); mutation.reset(); };
  const fields = columns[section];
  const rows = result.data?.items ?? [];
  const busy = batch.isPending || mutation.isPending;
  const selectAll = rows.length > 0 && rows.every(row => selected.has(String(row.id)));
  const runBatch = (action: string) => { if (window.confirm(`${actionLabels[action]}：选中的 ${selected.size} 条崩溃报告？`)) { setBatchMessage(""); batch.mutate(action); } };
  const exportPage = () => downloadCSV(`msime-${section}-page-${page}.csv`, [fields.map(([, label]) => label), ...rows.map(row => fields.map(([key]) => row[key]))]); const editable = section !== "downloads" && section !== "audit";
  const act = (action: string, item: Row) => {
    if (!window.confirm(`${actionLabels[action]}：${item.name || item.display_name || item.id}？${action.startsWith("delete") ? "\n将永久删除内容及关联下载/收藏/评分记录，无法恢复。" : ""}`)) return;
    mutation.mutate({ action, id: String(item.id) });
  };
  return <><PageHeader page={section} busy={result.isFetching} refresh={() => void result.refetch()} />
    {result.isError && <p className="notice error" role="alert">{errorMessage(result.error)}</p>}
    {mutation.isError && <p className="notice error" role="alert">{errorMessage(mutation.error)}</p>}
    {mutation.isSuccess && <p className="notice" role="status">操作已完成，并已记录审计日志。</p>}
    {batchMessage && <p className={batch.data?.failures.length ? "notice error" : "notice"} role="status">{batchMessage}</p>}
    <section className={`list${compact ? " compact" : ""}`}><fieldset className="list-controls" disabled={busy}><form className="toolbar" onSubmit={event => { event.preventDefault(); setQuery(search.trim()); setPage(1); }}><div><label className="sr-only" htmlFor="search">搜索记录</label><input id="search" type="search" maxLength={200} placeholder="搜索名称、ID、版本…" value={search} onChange={event => setSearch(event.target.value)} /><button type="submit"><Search size={16} aria-hidden="true" />搜索</button></div><span className="muted small">每页 50 条 · 最新优先</span></form>
      <div className="list-filters">
        {events && <><label>平台<input value={platform} maxLength={32} placeholder="如 windows、ios" onChange={event => { setPlatform(event.target.value.trim()); setPage(1); }} /></label><label>版本<input value={version} maxLength={64} placeholder="精确版本号" onChange={event => { setVersion(event.target.value.trim()); setPage(1); }} /></label></>}
        {section === "crashes" && <label>处理状态<select value={status} onChange={event => { setStatus(event.target.value); setPage(1); }}><option value="">全部状态</option><option value="open">待处理</option><option value="resolved">已处理</option></select></label>}
        {section === "audit" && <><label>操作<select value={auditAction} onChange={event => { setAuditAction(event.target.value); setPage(1); }}><option value="">全部操作</option>{Object.entries(actionLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label><label>操作者<input value={actor} maxLength={200} placeholder="邮箱或 subject" onChange={event => { setActor(event.target.value); setPage(1); }} /></label></>}
        <button type="button" onClick={reset} disabled={!filtered && !search}><RotateCcw size={16} aria-hidden="true" />重置筛选</button>
        <span className="muted small" role="status">{result.data ? `匹配 ${result.data.total.toLocaleString()} 条记录` : result.isError ? "查询失败" : "正在查询…"}</span>
      </div>
      <div className="table-options"><label><input type="checkbox" checked={compact} onChange={event => setCompact(event.target.checked)} />紧凑显示</label><button type="button" disabled={!rows.length || result.isFetching || result.isError} onClick={exportPage}><Download size={16} aria-hidden="true" />导出当前页 CSV</button><span className="muted small">{result.dataUpdatedAt ? `更新于 ${new Date(result.dataUpdatedAt).toLocaleTimeString("zh-CN")}` : ""}</span></div>
      </fieldset>
      {section === "crashes" && <div className="batch-toolbar"><span role="status">已选 {selected.size} 条</span><button type="button" disabled={!selected.size || busy || result.isFetching} onClick={() => runBatch("resolve_crash")}>批量标记已处理</button><button type="button" disabled={!selected.size || busy || result.isFetching} onClick={() => runBatch("reopen_crash")}>批量重新打开</button><button type="button" disabled={!selected.size || busy} onClick={() => setSelected(new Set())}>取消选择</button>{batch.isPending && <span role="status">正在处理，请稍候…</span>}</div>}
      <div className="table-wrap"><table aria-busy={result.isFetching}><thead><tr>{section === "crashes" && <th scope="col"><input type="checkbox" aria-label="选择当前页全部崩溃" checked={selectAll} disabled={busy || !rows.length || result.isFetching} onChange={event => setSelected(event.target.checked ? new Set(rows.map(row => String(row.id))) : new Set())} /></th>}{fields.map(([key, title]) => <th key={key} scope="col">{title}</th>)}{editable && <th scope="col">操作</th>}</tr></thead><tbody>
        {result.data?.items.map(item => <tr key={String(item.id)}>{section === "crashes" && <td><input type="checkbox" aria-label={`选择崩溃 ${item.id}`} checked={selected.has(String(item.id))} disabled={busy || result.isFetching} onChange={event => setSelected(previous => { const next = new Set(previous); if (event.target.checked) next.add(String(item.id)); else next.delete(String(item.id)); return next; })} /></td>}{fields.map(([key]) => <td key={key}><Cell item={item} field={key} /></td>)}{editable && <td className="actions">{editable && <button type="button" disabled={busy} onClick={() => setDetail(item)}>详情</button>}<ActionButton section={section} item={item} pending={busy} act={act} /></td>}</tr>)}
        {!result.data?.items.length && <tr><td className="empty" colSpan={fields.length + Number(editable) + Number(section === "crashes")}>{result.isPending ? "正在加载数据…" : result.isError ? "数据加载失败，请点击刷新重试。" : filtered ? "没有匹配的记录，请调整筛选条件。" : "暂无数据。数据接入后将在这里展示。"}</td></tr>}
      </tbody></table></div><div className="pagination"><span>第 {page} / {Math.max(1, Math.ceil((result.data?.total ?? 0) / 50))} 页 · 本页 {result.data?.items.length ?? 0} 条</span><div><button type="button" disabled={page === 1 || result.isFetching || busy} onClick={() => setPage(page - 1)}>上一页</button><button type="button" disabled={!result.data?.has_more || result.isFetching || busy || page >= 10000} onClick={() => setPage(page + 1)}>下一页</button></div></div>
    </section>{detail && (section === "users" ? <UserDetail id={String(detail.id)} close={() => setDetail(null)} /> : section === "skins" || section === "dictionaries" || section === "replies" ? <ContentDetail section={section} id={String(detail.id)} close={() => setDetail(null)} /> : <CrashDetail item={detail} close={() => setDetail(null)} />)}
  </>;
}
function Cell({ item, field }: { item: Row; field: string }) {
  if (field === "resolved") return <span className={item.resolved ? "badge" : "badge warning"}>{item.resolved ? "已处理" : "待处理"}</span>;
  let value = String(item[field] ?? "—");
  if (field === "created_at" || field === "updated_at") value = new Date(value).toLocaleString("zh-CN");
  if (field === "action") value = actionLabels[value] || value;
  return <span className="cell-text" title={value}>{value}</span>;
}
function ActionButton({ section, item, pending, act }: { section: ListPage; item: Row; pending: boolean; act: (action: string, item: Row) => void }) {
  const action = { users: "revoke_sessions", skins: "delete_skin", dictionaries: "delete_dictionary", replies: "delete_reply", crashes: item.resolved ? "reopen_crash" : "resolve_crash", downloads: "", audit: "" }[section];
  if (!action) return null;
  return <button type="button" className={action.startsWith("delete") ? "danger" : ""} disabled={pending} onClick={() => act(action, item)}>{actionLabels[action]}</button>;
}
function CrashDetail({ item, close }: { item: Row; close: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const [copied, setCopied] = useState(false);
  useEffect(() => { const dialog = ref.current; dialog?.showModal(); return () => dialog?.close(); }, []);
  const report = `${item.message}\n\n${item.platform} / ${item.version}\n事件 ID: ${item.id}\n\n${item.stack || "未提供堆栈"}`;
  return <dialog ref={ref} aria-labelledby="detail-title" onCancel={close}><div className="dialog-heading"><h2 id="detail-title">崩溃详情</h2><div className="actions"><button type="button" onClick={() => { void navigator.clipboard?.writeText(report).then(() => setCopied(true)); }}>复制报告</button><button type="button" onClick={close}>关闭</button></div></div>{copied && <p className="notice" role="status">报告已复制到剪贴板。</p>}<pre>{report}</pre></dialog>;
}
