import { useState } from "react";
import { Menu, X } from "lucide-react";
import { Link, Outlet, useLocation } from "@tanstack/react-router";
import { Icon } from "./icon";
import { Login, useAuth } from "./auth";
import { pages } from "./pages";
import type { Page } from "./pages";
import logo from "./assets/msime.png";

export function Shell() {
  const [menuOpen, setMenuOpen] = useState(false);
  const { authenticated, loading, error, session } = useAuth(); const path = useLocation({ select: value => value.pathname });
  if (loading) return <section className="grid min-h-screen place-items-center bg-[#f4f6f5] p-6"><div className="rounded-2xl border border-[#e0e8e3] bg-white p-8 text-sm text-[#72857c] shadow-lg" role="status">正在恢复登录状态…</div></section>;
  if (!authenticated) return <Login />;
  return <div className="min-h-screen bg-[#f4f6f5] text-[#182c25] md:pl-[240px]"><a className="fixed left-3 top-[-60px] z-20 rounded-lg bg-white px-3 py-2 text-[#245d44] shadow-lg focus:top-3" href="#main-content">跳转到主要内容</a><aside className="sticky top-0 z-10 flex w-full flex-col overflow-y-auto border-b border-[#e3e9e5] bg-[#102d25] px-4 py-3 text-[#d9e9df] shadow-xl max-md:max-h-[85dvh] md:fixed md:inset-y-0 md:left-0 md:w-[240px] md:border-0 md:px-4 md:py-6"><Link className="flex shrink-0 items-center gap-3 px-2 text-[21px] font-semibold text-[#f4fbf6] no-underline" to="/"><img className="h-12 w-12 shrink-0 object-contain" src={logo} alt="" /><div>水杉{session?.version && <span className="ml-2 rounded bg-[#1e4b3b] px-1.5 py-0.5 text-[10px] font-medium text-[#b7d8c4]" title={`服务版本 ${session.version}`}>v{session.version}</span>}<small className="mt-1 block text-[10px] font-normal tracking-[2px] text-[#86a99a]">管理控制台</small></div></Link><button type="button" className="absolute right-4 top-5 flex items-center gap-1 rounded-md border border-[#386151] bg-transparent px-2 py-1 text-xs text-[#d9e9df] md:hidden" aria-expanded={menuOpen} aria-controls="admin-navigation" onClick={() => setMenuOpen(!menuOpen)}>{menuOpen ? <X size={18} aria-hidden="true" /> : <Menu size={18} aria-hidden="true" />}导航</button>
    <nav id="admin-navigation" aria-label="后台导航" className={`${menuOpen ? "grid" : "hidden"} mt-5 gap-5 md:grid`}>{([
      ["数据与用户", ["overview", "users", "downloads", "crashes"]],
      ["社区内容", ["skins", "dictionaries", "replies"]],
      ["系统管理", ["admins", "audit", "system"]],
    ] as const).map(([group, keys]) => <div className="nav-group" key={group}><p className="mb-2 mt-4 px-3 text-[11px] font-bold uppercase tracking-[1px] text-[#73998a]">{group}</p>{keys.filter(key => key !== "admins" || session?.can_manage_admins).map(key => {
      const [title, , icon] = pages[key]; const active = key === "overview" ? path === "/" : path === `/${key}`;
      return <Link key={key} to={key === "overview" ? "/" : "/$section"} params={{ section: key }} className={`my-0.5 flex items-center gap-3 rounded-[10px] px-3.5 py-2.5 text-[13px] no-underline transition ${active ? "bg-[#d8f1e0] font-semibold text-[#164c35] shadow-md" : "text-[#aac4b8] hover:bg-[#1b4336] hover:text-[#e8f5ed]"}`} aria-current={active ? "page" : undefined} onClick={() => setMenuOpen(false)}><Icon className="h-5 w-5 shrink-0" name={icon} /><span>{title}</span></Link>;
    })}</div>)}</nav>
    <div className="mt-auto hidden border-t border-[#285142] pt-4 text-[10px] leading-6 tracking-wide text-[#9cbaad] md:block"><span className="mr-1 inline-block h-1.5 w-1.5 rounded-full bg-[#7de0a4]" /> MSIME BACKEND<br /><span className="text-[#9cbaad]">管理操作已启用审计</span></div>
  </aside><main id="main-content" className="min-h-screen bg-gradient-to-b from-[#f8faf8] to-[#f4f6f5] px-4 py-5 md:px-[clamp(20px,3vw,48px)] md:py-6" tabIndex={-1}>{session?.email && <div className="mb-5 flex h-[38px] items-center justify-end gap-2 text-xs text-[#53675d]"><span className="h-2 w-2 rounded-full bg-[#48b978] ring-4 ring-[#48b97820]" /><span className="rounded bg-[#e4f4e9] px-2 py-1 text-[#246443]">{session.can_manage_admins ? "超级管理员" : "管理员"}</span><span className="truncate" title={session.email}>{session.email}</span></div>}{error && <p className="rounded-lg border border-[#e5c8c5] bg-[#fff4f2] p-3 text-sm text-[#b44940]" role="alert">{error}</p>}<Outlet /><footer className="mt-8 flex justify-between gap-3 text-[10px] text-[#9aa59e] max-sm:flex-col">水杉管理控制台<span>统计趋势按 UTC 自然日汇总 · 时间显示为本地时间</span></footer></main></div>;
}
export function PageHeader({ page, refresh, busy }: { page: Page; refresh: () => void; busy: boolean }) {
  const { logout } = useAuth(); const [title, subtitle] = pages[page];
  return <header className="mb-7 flex flex-wrap items-center justify-between gap-4"><div><p className="text-[11px] font-semibold tracking-[1.5px] text-[#6b8d7c]">WORKSPACE / {page.toUpperCase()}</p><h1 className="my-2 text-[32px] font-semibold tracking-[-1px] text-[#132b22] max-sm:text-[25px]">{title}</h1><p className="m-0 text-[13px] leading-7 text-[#72857c]">{subtitle}</p></div><div className="flex shrink-0 gap-2"><button className="inline-flex items-center gap-1.5 rounded-lg border border-[#d6e3da] bg-white px-3.5 py-2 text-sm text-[#236342] shadow-sm transition hover:bg-[#edf4ef] disabled:cursor-not-allowed disabled:opacity-50" type="button" onClick={refresh} disabled={busy}><Icon name="refresh" />刷新</button><button className="rounded-lg border border-[#d6e3da] bg-white px-3.5 py-2 text-sm text-[#65786e] shadow-sm transition hover:bg-[#edf4ef]" type="button" onClick={() => void logout()}>退出</button></div></header>;
}
