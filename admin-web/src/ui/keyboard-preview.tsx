import { SkinPreview } from "../skin-preview";

// KeyboardPreview renders a community skin design (the JSON content of a skin) as the real keyboard SVG used by the client preview, with a 26 键 / 九键 toggle.
export function KeyboardPreview({ design, name }: { design: unknown; name: string }) {
  return <SkinPreview content={design} name={name} />;
}
