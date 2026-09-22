import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { Download } from "lucide-react";
import { downloadCSV } from "./export";
import { useQuery } from "@tanstack/react-query";
import { overviewSchema, errorMessage } from "./api";
import type { Overview } from "./api";
import { useAuth } from "./auth";
import { PageHeader } from "./shell";

export function OverviewPage() {
  const { api } = useAuth();
  const [days, setDays] = useState<7 | 30>(30);
  const query = useQuery({ queryKey: ["admin", "overview", days], queryFn: async ({ signal }) => overviewSchema.parse(await api(`overview?days=${days}`, signal)) });
  return <><PageHeader page="overview" refresh={() => void query.refetch()} busy={query.isFetching} />
    {query.isPending && <p className="notice" role="status">正在加载数据…</p>}
    {query.isError && <p className="notice error" role="alert">{errorMessage(query.error)}</p>}
    {query.data && <OverviewData data={query.data} days={days} setDays={setDays} />}</>;
}
function OverviewData({ data, days, setDays }: { data: Overview; days: 7 | 30; setDays: (days: 7 | 30) => void }) {
  const [metric, setMetric] = useState<"downloads" | "users" | "crashes">("downloads");
  const daily = data.daily;
  const metricLabels = { downloads: "下载上报", users: "新增用户", crashes: "崩溃上报" };
  const destinations = { users: "users", new_users_30d: "users", session_users: "users", downloads: "downloads", open_crashes: "crashes", skins: "skins", dictionaries: "dictionaries", replies: "replies", resource_saves: "dictionaries" };
  const cards = [
    ["users", "注册用户", "累计注册账户"], ["downloads", "安装包下载", "累计上报事件"],
    ["open_crashes", "待处理崩溃", `累计 ${data.crashes} 条报告`], ["skins", "社区皮肤", `${data.skin_downloads} 次用户去重下载`],
    ["dictionaries", "共享词库", "公开社区词库"], ["replies", "回复模板", "公开社区模板"],
  ] as const;
  const stats = [["new_users_30d", "近30天新用户"], ["session_users", "持有有效会话的用户"], ["dictionaries", "共享词库"], ["replies", "回复模板"], ["resource_saves", "资源收藏"]] as const;
  const max = Math.max(1, ...daily.map(day => day[metric]));
  return <section><div className="cards">{cards.map(([key, title, hint]) => <Link className="metric" key={key} to="/$section" params={{ section: destinations[key] }}><p className="muted">{title}</p><strong>{data[key].toLocaleString()}</strong><p className="small muted">{hint} · 查看记录 →</p></Link>)}</div>
    <article className="panel"><div className="panel-heading"><h2>近 {days} 天趋势</h2><div className="trend-controls"><label><span className="sr-only">趋势指标</span><select value={metric} onChange={event => setMetric(event.target.value as typeof metric)}>{Object.entries(metricLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label><label><span className="sr-only">趋势时间范围</span><select value={days} onChange={event => setDays(Number(event.target.value) as 7 | 30)}><option value={7}>近 7 天</option><option value={30}>近 30 天</option></select></label><button type="button" onClick={() => downloadCSV(`msime-trends-${days}d.csv`, [["日期 (UTC)", "新增用户", "下载上报", "崩溃上报"], ...daily.map(day => [day.day, day.users, day.downloads, day.crashes])])}><Download size={16} aria-hidden="true" />导出</button></div></div><div className="chart">
      <svg viewBox="0 0 900 160" role="img" aria-label={`最近 ${days} 天${metricLabels[metric]}柱状图，精确数值见下方每日明细`}><title>{metricLabels[metric]}趋势</title>{daily.map((day, index) => <rect key={day.day} x={index * (900 / days) + 4} y={150 - day[metric] / max * 145} width={900 / days - 10} height={day[metric] / max * 145} rx={3}><title>{day.day}: {day[metric]}</title></rect>)}</svg>
    </div><div className="axis"><span>{daily[0].day}</span><span>UTC</span><span>{daily.at(-1)?.day}</span></div></article>
    <div className="secondary-stats">{stats.map(([key, title]) => <Link key={key} to="/$section" params={{ section: destinations[key] }}><strong>{data[key].toLocaleString()}</strong><span className="muted small">{title} · 查看 →</span></Link>)}</div>
    <details className="panel"><summary>每日数据明细（近 {days} 天）</summary><div className="table-wrap"><table><thead><tr>{["日期 (UTC)", "新增用户", "下载上报", "崩溃上报"].map(title => <th key={title} scope="col">{title}</th>)}</tr></thead><tbody>{[...daily].reverse().map(day => <tr key={day.day}><td>{day.day}</td><td>{day.users}</td><td>{day.downloads}</td><td>{day.crashes}</td></tr>)}</tbody></table></div></details>
  </section>;
}
