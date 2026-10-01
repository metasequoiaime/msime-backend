import { useEffect, useState } from "react";
import { Outlet, useLocation } from "@tanstack/react-router";
import { useHotkeys } from "react-hotkeys-hook";
import { useAuth } from "../auth";
import { ErrorState } from "../ui/states";
import { Header } from "./header";
import { MOBILE_QUERY, useMediaQuery } from "./media";
import { PermissionsProvider } from "./permissions";
import { Sidebar } from "./sidebar";

const SIDE_KEY = "msime-admin-side";

// The collapsed rail is remembered in localStorage (msime-admin-side = "1" expanded / "0" collapsed); without a stored choice it starts collapsed below 900px.
function initialSide(): boolean {
  try {
    const stored = localStorage.getItem(SIDE_KEY);
    if (stored !== null) return stored === "1";
  } catch {
    // Storage may be unavailable; fall through to the width default.
  }
  return window.innerWidth >= 900;
}

export function AppShell() {
  const mobile = useMediaQuery(MOBILE_QUERY);
  const [side, setSide] = useState(initialSide);
  const [mobileOpen, setMobileOpen] = useState(false);
  const pathname = useLocation({ select: location => location.pathname });
  const { error } = useAuth();
  useEffect(() => { if (!mobile) setMobileOpen(false); }, [mobile]);
  useHotkeys("escape", () => setMobileOpen(false), { enabled: mobile && mobileOpen });
  const toggleSide = () => {
    if (mobile) {
      setMobileOpen(false);
      return;
    }
    const next = !side;
    try {
      localStorage.setItem(SIDE_KEY, next ? "1" : "0");
    } catch {
      // The rail still toggles for this page view without storage.
    }
    setSide(next);
  };
  const onNavigate = () => {
    setMobileOpen(false);
    window.scrollTo(0, 0);
  };
  return <PermissionsProvider>
    <a href="#main-content" className="fixed top-[-60px] left-3 z-90 rounded-lg bg-panel px-3 py-2 text-accent-ink shadow-pop focus:top-3">跳转到主要内容</a>
    <div className="flex min-h-screen text-body">
      {mobile && mobileOpen && <div aria-hidden="true" className="fixed inset-0 z-45 bg-[rgba(10,20,14,.35)]" onClick={() => setMobileOpen(false)} />}
      <Sidebar expanded={mobile || side} mobile={mobile} mobileOpen={mobileOpen} onToggle={toggleSide} onNavigate={onNavigate} />
      <div className="flex min-w-0 flex-1 flex-col">
        <Header mobile={mobile} onOpenNav={() => setMobileOpen(true)} />
        <main id="main-content" tabIndex={-1} className="mx-auto w-full max-w-[1440px] flex-1 p-[clamp(16px,2.4vw,28px)] outline-none">
          {error && <ErrorState className="mb-4" error={new Error(error)} />}
          <div key={pathname} className="animate-page-in"><Outlet /></div>
        </main>
      </div>
    </div>
  </PermissionsProvider>;
}
