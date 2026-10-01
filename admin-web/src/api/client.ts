import { createContext, useContext, useMemo } from "react";
import { z } from "zod";

export type Method = "GET" | "POST" | "PATCH" | "DELETE";

// Server errors arrive as {"error":{"code","message"}} from fail() or as {"error":"code"} from newer handlers; both carry a stable machine code.
const errorBodySchema = z.object({ error: z.union([z.string(), z.object({ code: z.string(), message: z.string().optional() })]) });

const codeMessages: Record<string, string> = {
  unauthorized: "登录已失效或凭据无效，请重新登录。",
  permission_denied: "当前角色没有执行此操作的权限。",
  owner_required: "只有配置中的所有者可以执行此操作。",
  github_disabled: "GitHub 集成未配置。",
  rate_limit_exceeded: "请求过于频繁，请稍后重试。",
  exists: "记录已存在。",
  protected: "该项受保护，不能修改。",
  conflict: "数据已被其他人修改，请刷新后重试。",
  not_found: "记录不存在，可能已被删除，请刷新列表。",
  invalid_request: "参数无效，请检查输入。",
  origin_denied: "请求来源被拒绝，请从管理后台域名访问。",
  account_banned: "该账号已被封禁。",
  blocked_word: "内容命中敏感词，已被拦截。",
  not_implemented: "该功能的服务端接口尚未实现。",
};

function statusMessage(status: number): string {
  if (status === 401) return codeMessages.unauthorized;
  if (status === 403) return "没有执行此操作的权限，或该账号受到保护。";
  if (status === 404) return codeMessages.not_found;
  if (status === 409) return "操作冲突：记录已存在或已被修改，请刷新后重试。";
  if (status === 400 || status === 422) return codeMessages.invalid_request;
  if (status === 429) return codeMessages.rate_limit_exceeded;
  return `请求失败 (${status})，请检查服务和数据库状态。`;
}

export class APIError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string) {
    super(codeMessages[code] ?? statusMessage(status));
    this.status = status;
    this.code = code;
  }
}

export type RequestOptions = {
  method?: Method;
  body?: unknown;
  signal?: AbortSignal;
  token?: string;
  // keepalive lets a request outlive the page; use it for delayed commits flushed on pagehide.
  keepalive?: boolean;
};

// requestAPI calls /api/<path> and validates the JSON response with the given zod schema. A body without an explicit method is sent as POST.
export async function requestAPI<S extends z.ZodType>(path: string, schema: S, options: RequestOptions = {}): Promise<z.infer<S>> {
  const { body, signal, token, keepalive } = options;
  const method = options.method ?? (body === undefined ? "GET" : "POST");
  const response = await fetch(`/api/${path.replace(/^\/+/, "")}`, {
    method, signal, keepalive, credentials: "same-origin",
    headers: { Accept: "application/json", ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (!response.ok) {
    let code = "";
    const text = await response.text();
    if (text) {
      try {
        const parsed = errorBodySchema.safeParse(JSON.parse(text));
        if (parsed.success) code = typeof parsed.data.error === "string" ? parsed.data.error : parsed.data.error.code;
      } catch {
        code = "";
      }
    }
    throw new APIError(response.status, code);
  }
  const text = await response.text();
  return schema.parse(text ? JSON.parse(text) : null);
}

export function errorMessage(error: unknown): string {
  if (error instanceof z.ZodError) return "服务返回的数据格式不正确，请检查前后端版本是否一致。";
  if (error instanceof DOMException && error.name === "AbortError") return "请求已取消。";
  return error instanceof Error ? error.message : "请求失败，请重试。";
}

export function isAPIError(error: unknown, code: string): boolean {
  return error instanceof APIError && error.code === code;
}

// isGithubDisabled marks the explicit 未配置 state of pages backed by the optional admin.github config.
export function isGithubDisabled(error: unknown): boolean {
  return error instanceof APIError && error.status === 404 && error.code === "github_disabled";
}

// Loose so extra fields that an action adds to the response (for example the id of a created row) reach the caller instead of being stripped.
export const actionResultSchema = z.looseObject({ ok: z.literal(true), affected: z.number() });
export type ActionResult = z.infer<typeof actionResultSchema>;

// ActionRequest is the body of POST /api/actions; the server rejects unknown fields.
export type ActionRequest = {
  action: string;
  id?: string;
  user_id?: string;
  ids?: string[];
  reason?: string;
  section?: string;
  value?: unknown;
};

type Credentials = { token: string; onUnauthorized: () => void };
export const APICredentialsContext = createContext<Credentials | null>(null);

type CallOptions = Omit<RequestOptions, "method" | "token" | "body">;

export type API = {
  get<S extends z.ZodType>(path: string, schema: S, options?: CallOptions): Promise<z.infer<S>>;
  post<S extends z.ZodType>(path: string, schema: S, body?: unknown, options?: CallOptions): Promise<z.infer<S>>;
  patch<S extends z.ZodType>(path: string, schema: S, body?: unknown, options?: CallOptions): Promise<z.infer<S>>;
  delete<S extends z.ZodType>(path: string, schema: S, options?: CallOptions): Promise<z.infer<S>>;
  action(body: ActionRequest, options?: CallOptions): Promise<ActionResult>;
};

// useAPI binds requestAPI to the signed-in admin (cookie session or in-memory legacy token) and signs out on 401.
export function useAPI(): API {
  const credentials = useContext(APICredentialsContext);
  if (!credentials) throw new Error("AuthProvider required");
  const { token, onUnauthorized } = credentials;
  return useMemo(() => {
    const call = async <S extends z.ZodType>(path: string, schema: S, options: RequestOptions): Promise<z.infer<S>> => {
      try {
        return await requestAPI(path, schema, { ...options, token });
      } catch (error) {
        if (error instanceof APIError && error.status === 401) onUnauthorized();
        throw error;
      }
    };
    return {
      get: (path, schema, options) => call(path, schema, { ...options, method: "GET" }),
      post: (path, schema, body, options) => call(path, schema, { ...options, method: "POST", body: body ?? {} }),
      patch: (path, schema, body, options) => call(path, schema, { ...options, method: "PATCH", body: body ?? {} }),
      delete: (path, schema, options) => call(path, schema, { ...options, method: "DELETE" }),
      action: (body, options) => call("actions", actionResultSchema, { ...options, method: "POST", body }),
    };
  }, [token, onUnauthorized]);
}
