import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { Chart } from "@tanstack/charts/react/tooltip";
import { defineChart, lineY } from "@tanstack/charts";
import { tooltip } from "@tanstack/charts/tooltip";
import { scaleLinear } from "@tanstack/charts/scales/linear";
import { scalePoint } from "@tanstack/charts/scales/point";
import { Activity, AlertTriangle, ArrowUpRight, BookOpen, Download, MessageSquare, Palette, ShieldCheck, TrendingUp, Users } from "lucide-react";
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
    ["users", "注册用户", "累计注册账户", Users, "blue"], ["downloads", "安装包下载", "累计上报事件", Download, "violet"],
    ["open_crashes", "待处理崩溃", `累计 ${data.crashes} 条报告`, AlertTriangle, data.open_crashes ? "amber" : "green"], ["skins", "社区皮肤", `${data.skin_downloads} 次用户去重下载`, Palette, "pink"],
    ["dictionaries", "共享词库", "公开社区词库", BookOpen, "teal"], ["replies", "回复模板", "公开社区模板", MessageSquare, "orange"],
  ] as const;
  const stats = [["new_users_30d", "近30天新用户"], ["session_users", "持有有效会话的用户"], ["dictionaries", "共享词库"], ["replies", "回复模板"], ["resource_saves", "资源收藏"]] as const;
  const chartDefinition = useMemo(() => defineChart({ marks: [lineY(daily, { x: "day", y: metric, points: true, stroke: "#3f9561", strokeWidth: 3 })], scales: { x: { scale: () => scalePoint<string>().padding(0.18), axis: { ticks: { format: (value: string) => value.slice(5) } } }, y: { scale: scaleLinear, nice: true, grid: true, axis: { ticks: { format: (value: number) => value.toLocaleString() } } } }, svgAnimation: true, tooltip }), [daily, metric]);
  return <section className="overview-page"><div className="overview-hero"><div><span className="hero-kicker"><Activity size={14} />运营概览</span><h2>今天的工作台</h2><p>把产品健康度、社区活跃和风险信号放在一个视图里。</p></div><div className="hero-health"><span className="health-orb"><ShieldCheck size={20} /></span><div><strong>系统运行正常</strong><small>最近一次检查刚刚完成</small></div></div></div><div className="cards">{cards.map(([key, title, hint, CardIcon, tone]) => <Link className={`metric metric-${tone}`} key={key} to="/$section" params={{ section: destinations[key] }}><div className="metric-top"><span className="metric-icon"><CardIcon size={17} /></span><ArrowUpRight size={16} className="metric-arrow" /></div><p className="muted">{title}</p><strong>{data[key].toLocaleString()}</strong><p className="small muted">{hint}</p></Link>)}</div>
    <article className="panel trend-panel"><div className="panel-heading"><div><span className="section-kicker"><TrendingUp size={13} />活动趋势</span><h2>近 {days} 天数据变化</h2></div><div className="trend-controls"><label><span className="sr-only">趋势指标</span><select value={metric} onChange={event => setMetric(event.target.value as typeof metric)}>{Object.entries(metricLabels).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label><label><span className="sr-only">趋势时间范围</span><select value={days} onChange={event => setDays(Number(event.target.value) as 7 | 30)}><option value={7}>近 7 天</option><option value={30}>近 30 天</option></select></label><button type="button" onClick={() => downloadCSV(`msime-trends-${days}d.csv`, [["日期 (UTC)", "新增用户", "下载上报", "崩溃上报"], ...daily.map(day => [day.day, day.users, day.downloads, day.crashes])])}><Download size={16} aria-hidden="true" />导出数据</button></div></div><div className="chart tanstack-chart"><Chart definition={chartDefinition} height={270} ariaLabel={`最近 ${days} 天${metricLabels[metric]}趋势图`} renderTooltipBody={({ defaultBody }) => <div className="chart-tooltip">{defaultBody}</div>} /></div><div className="axis"><span>{daily[0]?.day}</span><span>UTC · 可悬停查看每日明细</span><span>{daily.at(-1)?.day}</span></div></article>
    <div className="secondary-stats">{stats.map(([key, title]) => <Link key={key} to="/$section" params={{ section: destinations[key] }}><span className="secondary-label">{title}</span><strong>{data[key].toLocaleString()}</strong><span className="muted small">查看明细 <ArrowUpRight size={13} /></span></Link>)}</div>
    <details className="panel"><summary>每日数据明细（近 {days} 天）</summary><div className="table-wrap"><table><thead><tr>{["日期 (UTC)", "新增用户", "下载上报", "崩溃上报"].map(title => <th key={title} scope="col">{title}</th>)}</tr></thead><tbody>{[...daily].reverse().map(day => <tr key={day.day}><td>{day.day}</td><td>{day.users}</td><td>{day.downloads}</td><td>{day.crashes}</td></tr>)}</tbody></table></div></details>
  </section>;
}
