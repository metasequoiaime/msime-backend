import type { ReactNode } from "react";
import type { PageKey } from "../nav";
import { navItem } from "../nav";

// PageIntro is the one-line page description shown above page content; the page title itself lives in the header.
export function PageIntro({ page, children }: { page: PageKey; children?: ReactNode }) {
  return <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
    <p className="m-0 text-[13px] text-muted">{navItem(page).description}</p>
    {children && <div className="flex flex-wrap items-center gap-2">{children}</div>}
  </div>;
}
