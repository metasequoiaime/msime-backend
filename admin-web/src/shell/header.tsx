import { useLocation } from "@tanstack/react-router";
import { Menu } from "lucide-react";
import { navGroupTitle, pageForPath } from "../nav";
import { Button } from "../ui/button";
import { AppearanceMenu } from "./appearance";
import { NotificationBell } from "./notifications";
import { GlobalSearch } from "./search";
import { useShell } from "./shell-data";

export function Header({ mobile, onOpenNav }: { mobile: boolean; onOpenNav: () => void }) {
  const pathname = useLocation({ select: location => location.pathname });
  const page = pageForPath(pathname);
  const shell = useShell();
  return <header className="sticky top-0 z-20 flex h-16 shrink-0 items-center gap-3 border-b border-hair bg-bg px-[clamp(16px,2.4vw,28px)]">
    {mobile && <Button variant="outline" size="icon" title="菜单" aria-label="打开导航" aria-controls="admin-navigation" onClick={onOpenNav}><Menu size={18} aria-hidden="true" /></Button>}
    <div className="min-w-0">
      {page && <div className="text-xs text-muted">{navGroupTitle(page.key)}</div>}
      <h1 className="m-0 truncate text-[17px] leading-tight font-bold whitespace-nowrap text-ink">{page?.label ?? "页面不存在"}</h1>
    </div>
    <div className="ml-auto flex items-center gap-2">
      <div className="hidden min-[900px]:block"><GlobalSearch /></div>
      <NotificationBell unread={shell.data?.unread_notifications ?? 0} />
      {!mobile && shell.data?.environment && <span className="inline-flex h-9 items-center gap-2 rounded-[10px] px-3 text-[13px] whitespace-nowrap text-body inset-ring inset-ring-hair-2"><span aria-hidden="true" className="h-2 w-2 rounded-full bg-accent" />{shell.data.environment}</span>}
      <AppearanceMenu />
    </div>
  </header>;
}
