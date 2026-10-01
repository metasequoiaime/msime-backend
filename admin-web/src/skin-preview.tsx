import { useId, useState } from "react";
import { z } from "zod";
import { Delete, Globe, CornerDownLeft, ArrowUp } from "lucide-react";
import { Segmented } from "./ui/segmented";

const color = z.number().int().min(0).max(0xffffff);
const designSchema = z.object({
  background: color, keyBackground: color, keyForeground: color, accent: color, actionBackground: color,
  cornerRadius: z.number().min(0).max(20), borderWidth: z.number().min(0).max(2), shadow: z.number().min(0).max(.4), pattern: z.number().int().min(0).max(3), monospaced: z.boolean(),
  keyShape: z.enum(["", "rounded", "capsule", "ticket", "pebble"]).optional(), keyMaterial: z.enum(["", "flat", "raised", "glass", "paper"]).optional(),
  keyOpacity: z.number().min(.25).max(1).nullish(), gradientEnd: color.nullish(), gradientHorizontal: z.boolean().nullish(),
  patternOpacity: z.number().min(0).max(.5).nullish(), customBorderColor: color.nullish(),
  photo: z.string().max(683000).regex(/^[A-Za-z0-9+/]*={0,2}$/).nullish(), photoShade: z.number().min(0).max(.8).nullish(), photoPosition: z.number().min(0).max(1).nullish(),
});
const hex = (value: number) => `#${value.toString(16).padStart(6, "0")}`;
const foreground = (value: number) => { const r = value >> 16, g = (value >> 8) & 255, b = value & 255; return r * .299 + g * .587 + b * .114 > 150 ? "#17251d" : "#ffffff"; };

export function SkinPreview({ content, name }: { content: unknown; name: string }) {
  const [layout, setLayout] = useState<"full" | "nine">("full"); const prefix = useId();
  const parsed = designSchema.safeParse(content);
  if (!parsed.success) return <p className="rounded-[14px] bg-bad-soft px-4 py-3 text-[13.5px] text-bad" role="status">该皮肤设计格式暂不支持预览，请展开原始数据检查。</p>;
  const d = parsed.data; const bg = hex(d.background), key = hex(d.keyBackground), ink = hex(d.keyForeground), accent = hex(d.accent);
  const radius = d.keyShape === "capsule" ? 24 : d.keyShape === "pebble" ? 18 : d.keyShape === "ticket" ? 3 : d.cornerRadius;
  const patternID = `${prefix}-pattern`, gradientID = `${prefix}-gradient`, materialID = `${prefix}-material`, clipID = `${prefix}-clip`;
  const row = (labels: string[], y: number, start: number, width: number) => labels.map((label, i) => drawKey(label, start + i * (width + 5), y, width));
  function drawKey(label: string, x: number, y: number, width: number, action = false) {
    const fill = action ? hex(d.actionBackground) : key; const text = action ? foreground(d.actionBackground) : ink;
    const raised = d.keyMaterial === "raised"; const shadow = raised ? Math.max(.2, d.shadow) : d.shadow;
    return <g key={`${x}-${y}`}>
      <rect x={x} y={y + (raised ? 3 : 2)} width={width} height={43} rx={radius} fill="#000000" opacity={shadow} />
      <rect x={x} y={y} width={width} height={43} rx={radius} fill={fill} fillOpacity={d.keyOpacity ?? 1} stroke={hex(d.customBorderColor ?? d.keyForeground)} strokeOpacity={d.customBorderColor == null ? .15 : 1} strokeWidth={d.borderWidth} />
      {d.keyMaterial === "glass" && <rect x={x} y={y} width={width} height={43} rx={radius} fill={`url(#${materialID})`} />}
      {d.keyMaterial === "paper" && <path d={`M${x + 5} ${y + 36}h${Math.max(0, width - 10)}`} stroke={text} opacity={.12} />}
      {label === "删除" ? <Delete x={x + width / 2 - 10} y={y + 11} width={20} height={20} color={text} aria-hidden="true" /> : label === "换行" ? <CornerDownLeft x={x + width / 2 - 10} y={y + 11} width={20} height={20} color={text} aria-hidden="true" /> : label === "切换" ? <Globe x={x + width / 2 - 10} y={y + 11} width={20} height={20} color={text} aria-hidden="true" /> : label === "大写" ? <ArrowUp x={x + width / 2 - 10} y={y + 11} width={20} height={20} color={text} aria-hidden="true" /> : <text x={x + width / 2} y={y + 27} textAnchor="middle" fill={text} fontSize={label.length > 1 ? 12 : 17}>{label}</text>}
    </g>;
  }
  return <section className="my-4" aria-label={`${name}外观预览`}>
    <div className="flex flex-wrap items-center justify-between gap-3"><h3 className="m-0 text-[13px] font-bold text-ink">键盘预览</h3><Segmented size="sm" label="键盘布局" value={layout} onChange={setLayout} options={[{ value: "full", label: "26 键" }, { value: "nine", label: "九键" }]} /></div>
    <div className="mt-3 grid place-items-center rounded-xl bg-panel-2 p-5"><svg className="block h-auto w-full max-w-[440px] drop-shadow-xl" viewBox="0 0 390 290" role="img" aria-label={`${name}，${layout === "full" ? "26 键" : "九键"}键盘效果`} fontFamily={d.monospaced ? "ui-monospace, monospace" : "system-ui, sans-serif"}>
      <title>{name}键盘示意预览</title><defs>
        <linearGradient id={gradientID} x1="0" y1="0" x2={d.gradientHorizontal ? "1" : "0"} y2={d.gradientHorizontal ? "0" : "1"}><stop stopColor={bg} /><stop offset="1" stopColor={hex(d.gradientEnd ?? d.background)} /></linearGradient>
        <linearGradient id={materialID} x2="0" y2="1"><stop stopColor="#ffffff" stopOpacity=".45" /><stop offset="1" stopColor="#ffffff" stopOpacity="0" /></linearGradient>
        <pattern id={patternID} width="16" height="16" patternUnits="userSpaceOnUse">{d.pattern === 1 ? <path d="M0 16L16 0M-4 4L4 -4M12 20L20 12" stroke={accent} strokeWidth="1" /> : d.pattern === 2 ? <path d="M0 0H16M0 0V16" stroke={accent} strokeWidth="1" /> : <circle cx="8" cy="8" r="1.5" fill={accent} />}</pattern>
        <clipPath id={clipID}><rect width="390" height="290" rx="14" /></clipPath>
      </defs><g clipPath={`url(#${clipID})`}>
        <rect width="390" height="290" fill={`url(#${gradientID})`} />
        {d.photo && <><image href={`data:image/jpeg;base64,${d.photo}`} width="390" height="290" preserveAspectRatio={`xMidY${(d.photoPosition ?? .5) < .33 ? "Min" : (d.photoPosition ?? .5) > .66 ? "Max" : "Mid"} slice`} /><rect width="390" height="290" fill="#000000" opacity={d.photoShade ?? 0} /></>}
        {d.pattern > 0 && <rect width="390" height="290" fill={`url(#${patternID})`} opacity={d.patternOpacity ?? .12} />}
        <text x="14" y="22" fontSize="12" fill={ink}>shui shan</text><path d="M82 10V24" stroke={accent} />
        <text x="14" y="51" fontSize="19" fill={accent}>水杉</text><text x="83" y="51" fontSize="17" fill={ink}>水山</text><text x="148" y="51" fontSize="17" fill={ink}>水杉输入法</text>
        <path d="M12 64H378" stroke={ink} opacity=".12" />
        {layout === "full" ? <>{row([..."QWERTYUIOP"], 74, 7, 33.1)}{row([..."ASDFGHJKL"], 124, 25, 33.1)}{drawKey("大写", 7, 174, 42)}{row([..."ZXCVBNM"], 174, 54, 35)}{drawKey("删除", 334, 174, 49)}</> : <>{row(["符号", "ABC", "DEF"], 74, 7, 88)}{row(["GHI", "JKL", "MNO"], 124, 7, 88)}{row(["PQRS", "TUV", "WXYZ"], 174, 7, 88)}{drawKey("删除", 286, 74, 97)}{drawKey("分词", 286, 124, 97)}{drawKey("选词", 286, 174, 97, true)}</>}
        {drawKey("123", 7, 224, 46)}{drawKey("切换", 58, 224, 39)}{drawKey("空格", 102, 224, 183)}{drawKey("换行", 290, 224, 93, true)}
      </g>
    </svg></div><p className="m-0 mt-2 text-xs leading-relaxed text-muted">根据皮肤参数渲染的示意效果；字体、纹理和材质细节可能与客户端略有差异。</p>
  </section>;
}
