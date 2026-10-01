import { z } from "zod";

export const sensitiveCategories = ["ad", "vulgar", "abuse", "illegal", "custom"] as const;
export type SensitiveCategory = (typeof sensitiveCategories)[number];
export const sensitiveLevels = ["block", "review"] as const;
export type SensitiveLevel = (typeof sensitiveLevels)[number];

export const categoryLabels: Record<SensitiveCategory, string> = { ad: "导流广告", vulgar: "低俗", abuse: "辱骂", illegal: "违法违规", custom: "自定义" };
export const levelLabels: Record<SensitiveLevel, string> = { block: "直接拦截", review: "转人工" };

// One row of GET /api/sensitive-words. A regex pattern is stored without its surrounding slashes; hits_7d counts matches over the last seven UTC days.
export const sensitiveWordSchema = z.object({
  id: z.number().int().positive(),
  pattern: z.string(),
  is_regex: z.boolean(),
  category: z.enum(sensitiveCategories),
  level: z.enum(sensitiveLevels),
  created_by: z.string(),
  created_at: z.string(),
  hits_7d: z.number().int().nonnegative(),
});
export type SensitiveWord = z.infer<typeof sensitiveWordSchema>;

export const sensitiveWordsSchema = z.object({
  items: z.array(sensitiveWordSchema),
  max_hits: z.number().int().nonnegative(),
});
export type SensitiveWords = z.infer<typeof sensitiveWordsSchema>;

// add_sensitive_word answers with the new row's id.
export const addSensitiveWordResultSchema = z.object({ ok: z.literal(true), affected: z.number(), id: z.number().int().positive() });

// parsePatternInput mirrors the server: text written as /.../ is a regex stored without the slashes, anything else a plain word.
export function parsePatternInput(text: string): { pattern: string; isRegex: boolean } {
  const trimmed = text.trim();
  return /^\/.+\/$/s.test(trimmed) ? { pattern: trimmed.slice(1, -1), isRegex: true } : { pattern: trimmed, isRegex: false };
}

// displayPattern writes a regex back in the /.../ form the add bar accepts.
export function displayPattern(word: Pick<SensitiveWord, "pattern" | "is_regex">): string {
  return word.is_regex ? `/${word.pattern}/` : word.pattern;
}
