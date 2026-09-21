import { useState } from "react";
import { Menu, X } from "lucide-react";
import { Link, Outlet, useLocation } from "@tanstack/react-router";
import { Icon } from "./icon";
import { Login, useAuth } from "./auth";
import { pages } from "./pages";
import type { Page } from "./pages";
import logo from "./assets/msime.svg";

export function Shell() {
  const [menuOpen, setMenuOpen] = useState(false);
  const { authenticated, loading, error, session } = useAuth(); const path = useLocation({ select: value => value.pathname });
  if (loading) return <section className="login"><div className="login-panel" role="status">正在恢复登录状态…</div></section>;
  if (!authenticated) return <Login />;
  return <div className="app-shell"><a className="skip-link" href="#main-content">跳转到主要内容</a><aside><Link className="brand" to="/"><img className="brand-icon" src={logo} alt="" /><div>水杉{session?.version && <span className="brand-version" title={`服务版本 ${session.version}`}>v{session.version}</span>}<small>管理控制台</small></div></Link><button type="button" className="mobile-menu" aria-expanded={menuOpen} aria-controls="admin-navigation" onClick={() => setMenuOpen(!menuOpen)}>{menuOpen ? <X size={18} aria-hidden="true" /> : <Menu size={18} aria-hidden="true" />}导航</button>
    <nav id="admin-navigation" aria-label="后台导航" className={menuOpen ? "is-open" : ""}>{([
      ["数据与用户", ["overview", "users", "downloads", "crashes"]],
      ["社区内容", ["skins", "dictionaries", "replies"]],
      ["系统管理", ["admins", "audit"]],
    ] as const).map(([group, keys]) => <div className="nav-group" key={group}><p className="nav-label">{group}</p>{keys.filter(key => key !== "admins" || session?.can_manage_admins).map(key => {
      const [title, , icon] = pages[key]; const active = key === "overview" ? path === "/" : path === `/${key}`;
      return <Link key={key} to={key === "overview" ? "/" : "/$section"} params={{ section: key }} className={active ? "active" : ""} aria-current={active ? "page" : undefined} onClick={() => setMenuOpen(false)}><Icon className="nav-icon" name={icon} /><span>{title}</span></Link>;
    })}</div>)}</nav>
    <div className="sidebar-foot"><span className="dot" /> MSIME BACKEND<br /><span className="muted">管理操作已启用审计</span></div>
  </aside><main id="main-content" tabIndex={-1}>{session?.email && <div className="account-bar"><span className="badge">{session.can_manage_admins ? "超级管理员" : "管理员"}</span><span title={session.email}>{session.email}</span></div>}{error && <p className="notice error" role="alert">{error}</p>}<Outlet /><footer>水杉管理控制台<span>统计趋势按 UTC 自然日汇总 · 时间显示为本地时间</span></footer></main></div>;
}
export function PageHeader({ page, refresh, busy }: { page: Page; refresh: () => void; busy: boolean }) {
  const { logout } = useAuth(); const [title, subtitle] = pages[page];
  return <header><div><p className="eyebrow">WORKSPACE / {page.toUpperCase()}</p><h1>{title}</h1><p className="muted">{subtitle}</p></div><div className="header-actions"><button type="button" onClick={refresh} disabled={busy}><Icon name="refresh" />刷新</button><button type="button" onClick={() => void logout()}>退出</button></div></header>;
}
