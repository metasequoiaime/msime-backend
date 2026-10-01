import { Popover, ToggleGroup } from "radix-ui";
import { cn } from "../ui/cn";
import { Segmented } from "../ui/segmented";
import { resolveSeason, seasonForDate, seasonNames, setAppearance, useAppearance } from "./theme";
import type { SeasonChoice, ThemeMode } from "./theme";

const seasonTiles: readonly { key: SeasonChoice; label: string; swatch: string }[] = [
  { key: "auto", label: "自动", swatch: "bg-[conic-gradient(#4A9A3C_0_25%,#1E8E4E_0_50%,#B9582B_0_75%,#2E6F7A_0)]" },
  { key: "spring", label: "春", swatch: "bg-[#4A9A3C]" },
  { key: "summer", label: "夏", swatch: "bg-[#1E8E4E]" },
  { key: "autumn", label: "秋", swatch: "bg-[#B9582B]" },
  { key: "winter", label: "冬", swatch: "bg-[#2E6F7A]" },
];

const modes: readonly { value: ThemeMode; label: string }[] = [{ value: "light", label: "浅色" }, { value: "dark", label: "深色" }, { value: "system", label: "跟随系统" }];

// AppearanceMenu picks the season palette and light/dark mode; choices persist in localStorage (msime-admin-theme) and apply immediately.
export function AppearanceMenu() {
  const appearance = useAppearance();
  const label = seasonNames[resolveSeason(appearance.season)] + (appearance.season === "auto" ? " · 自动" : "");
  return <Popover.Root>
    <Popover.Trigger asChild>
      <button type="button" title="外观" aria-label={`外观：${label}`} className="inline-flex h-9 items-center gap-2 rounded-[10px] px-3 text-[13px] text-body inset-ring inset-ring-hair-2 transition hover:bg-panel-2">
        <span aria-hidden="true" className="h-2.5 w-2.5 rounded-full bg-accent" />{label}
      </button>
    </Popover.Trigger>
    <Popover.Portal>
      <Popover.Content align="end" sideOffset={8} collisionPadding={16} className="z-40 w-72 max-w-[calc(100vw-32px)] animate-pop-in rounded-2xl bg-panel p-4 shadow-pop outline-none">
        <div className="mb-2 text-[12.5px] font-semibold text-muted" id="season-label">季节皮肤</div>
        <ToggleGroup.Root type="single" value={appearance.season} onValueChange={next => { if (next) setAppearance({ season: next as SeasonChoice }); }} aria-labelledby="season-label" className="grid grid-cols-5 gap-1.5">
          {seasonTiles.map(tile => <ToggleGroup.Item key={tile.key} value={tile.key}
            className="flex flex-col items-center gap-1.5 rounded-[10px] pt-2.5 pb-2 text-xs text-body inset-ring inset-ring-hair-2 transition hover:bg-panel-2 data-[state=on]:font-semibold data-[state=on]:text-accent-ink data-[state=on]:inset-ring-[1.5px] data-[state=on]:inset-ring-accent">
            <span aria-hidden="true" className={cn("h-5 w-5 rounded-full", tile.swatch)} />{tile.label}
          </ToggleGroup.Item>)}
        </ToggleGroup.Root>
        <p className="m-0 mt-2.5 text-xs leading-[1.7] text-muted">自动按月份切换：3–5 月春，6–8 月夏，9–11 月秋，12–2 月冬。现在是{seasonNames[seasonForDate(new Date())]}。</p>
        <div className="mt-3.5 mb-2 text-[12.5px] font-semibold text-muted">明暗</div>
        <Segmented label="明暗" className="flex w-full" value={appearance.theme} onChange={theme => setAppearance({ theme })} options={modes} />
      </Popover.Content>
    </Popover.Portal>
  </Popover.Root>;
}
