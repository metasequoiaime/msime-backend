import { createContext, useContext, useState, useCallback, useEffect } from "react";
import type { ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { z } from "zod";
import { Icon } from "./icon";
import { APIError, errorMessage, overviewSchema, requestAPI } from "./api";
import logo from "./assets/msime.png";

const sessionSchema = z.object({ version: z.string().optional(), authenticated: z.boolean(), email: z.string(), google_enabled: z.boolean(), token_enabled: z.boolean(), can_manage_admins: z.boolean().default(false) });
type Session = z.infer<typeof sessionSchema>;
type Auth = { authenticated: boolean; loading: boolean; session: Session | null; error: string; logout: () => Promise<void>; login: (token: string) => Promise<void>; reload: () => void; api: (path: string, signal?: AbortSignal, body?: unknown) => Promise<unknown> };
const AuthContext = createContext<Auth | null>(null);
export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState("");
  const [session, setSession] = useState<Session | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const client = useQueryClient();
  useEffect(() => {
    const controller = new AbortController(); setLoading(true); setError("");
    requestAPI("", "auth/session", controller.signal).then(value => { if (!controller.signal.aborted) setSession(sessionSchema.parse(value)); }).catch(err => { if (!controller.signal.aborted) setError(errorMessage(err)); }).finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, []);
  const clear = useCallback(() => { setToken(""); setSession(previous => previous ? { ...previous, authenticated: false, email: "" } : previous); client.clear(); }, [client]);
  const logout = async () => {
    setError("");
    try { await requestAPI(token, "auth/logout", undefined, {}); clear(); }
    catch (err) { setError(errorMessage(err)); }
  };
  const login = async (value: string) => {
    const data = overviewSchema.parse(await requestAPI(value, "overview"));
    client.clear(); client.setQueryData(["admin", "overview"], data); setToken(value); setError("");
    setSession(previous => previous ? { ...previous, authenticated: true, email: "" } : previous);
  };
  const api = async (path: string, signal?: AbortSignal, body?: unknown) => {
    try { return await requestAPI(token, path, signal, body); }
    catch (err) { if (err instanceof APIError && err.status === 401) { clear(); setError("登录已失效，请重新登录。"); } throw err; }
  };
  return <AuthContext.Provider value={{ authenticated: session?.authenticated ?? false, loading, session, error, logout, login, reload: () => window.location.reload(), api }}>{children}</AuthContext.Provider>;
}
export function useAuth() { const value = useContext(AuthContext); if (!value) throw new Error("AuthProvider required"); return value; }

export function Login() {
  const { login, session, error: authError, reload } = useAuth();
  const [value, setValue] = useState(""); const [pending, setPending] = useState(false); const [error, setError] = useState("");
  const denied = new URLSearchParams(window.location.search).has("login_error");
  return <section className="grid min-h-screen bg-white lg:grid-cols-2"><div className="mx-auto flex w-full max-w-[460px] flex-col justify-center px-7 py-12 sm:px-12"><img className="mb-7 h-16 w-16 object-contain" src={logo} alt="" /><p className="text-[10px] font-semibold tracking-[2px] text-[#779185]">MSIME / ADMIN</p><h1 className="my-3 text-3xl font-semibold tracking-[-1px] text-[#132b22]">水杉管理控制台</h1><p className="text-sm leading-7 text-[#72857c]">了解产品使用情况，管理社区内容。</p>
    {session?.google_enabled && <div className="my-8"><a className="flex items-center justify-center gap-3 rounded-md border border-[#747775] bg-white px-3 py-3 text-sm font-medium text-[#1f1f1f] no-underline transition hover:bg-[#f2f5f3]" href="/api/auth/google/start"><svg width="20" height="20" viewBox="0 0 24 24" aria-hidden="true"><path fill="#4285F4" d="M21.6 12.23c0-.71-.06-1.39-.18-2.05H12v3.88h5.38a4.6 4.6 0 0 1-2 3.02v2.51h3.24c1.9-1.74 2.98-4.31 2.98-7.36Z"/><path fill="#34A853" d="M12 22c2.7 0 4.96-.9 6.62-2.41l-3.24-2.51c-.9.6-2.06.96-3.38.96-2.6 0-4.8-1.76-5.59-4.12H3.07v2.59A10 10 0 0 0 12 22Z"/><path fill="#FBBC05" d="M6.41 13.92a6 6 0 0 1 0-3.84V7.49H3.07a10 10 0 0 0 0 9.02l3.34-2.59A5.99 5.99 0 0 1 12 5.96Z"/><path fill="#EA4335" d="M12 5.96c1.47 0 2.79.5 3.82 1.49l2.87-2.87A9.6 9.6 0 0 0 12 2a10 10 0 0 0-8.93 5.49l3.34 2.59A5.99 5.99 0 0 1 12 5.96Z"/></svg>使用 Google 账号登录</a><p className="mt-3 text-xs leading-6 text-[#7b8983]">仅限已授权的管理员账号。登录会话有效期为 8 小时。</p></div>}
    {session?.token_enabled && <details className="mt-8" open={!session.google_enabled}><summary className="cursor-pointer text-xs text-[#6f8077]">管理员密钥登录</summary><form className="mt-4 grid gap-3" onSubmit={async (event) => { event.preventDefault(); setPending(true); setError(""); try { await login(value.trim()); setValue(""); } catch (e) { setError(errorMessage(e)); } finally { setPending(false); } }}>
      <label className="text-xs text-[#30473d]" htmlFor="token">管理员密钥</label><input className="rounded-lg border border-[#d6e1db] bg-white px-3 py-3 text-sm text-[#203a30] outline-none focus:ring-2 focus:ring-[#8db8a0]" id="token" type="password" required autoComplete="off" value={value} onChange={event => setValue(event.target.value)} placeholder="输入管理员密钥" />
      <button className="flex items-center justify-between rounded-lg bg-[#245d44] px-4 py-3 text-sm text-white transition hover:bg-[#174c35] disabled:opacity-50" type="submit" disabled={pending}>{pending ? "正在验证…" : "进入控制台"}<Icon name="arrow-right" /></button>
    </form><p className="mt-3 text-xs leading-6 text-[#7b8983]">密钥仅保留在当前页面内存中，刷新后需重新输入。</p></details>}
    {(denied || error || authError) && <p className="mt-5 text-sm text-[#b44940]" role="alert">{denied ? "Google 登录未完成或账号未获授权，请使用管理员账号重试。" : error || authError}</p>}
    {!session && <button className="mt-5 rounded-lg border border-[#d6e1db] px-3 py-2 text-sm" type="button" onClick={reload}>重新加载登录方式</button>}
  </div><div className="relative hidden overflow-hidden bg-[#193f31] p-16 text-[#e2ebe0] lg:flex lg:flex-col lg:justify-end"><div className="absolute left-12 top-[-250px] h-[800px] w-[800px] rounded-full border border-[#45614d] shadow-[0_0_0_65px_#ffffff04,0_0_0_130px_#ffffff04,0_0_0_195px_#ffffff04,0_0_0_260px_#ffffff04]" /><p className="relative text-5xl font-light leading-[1.5]">让每一次输入<br />都更自然。</p><span className="text-xs tracking-[3px] text-[#83a18e]">水杉输入法 · MSIME</span></div></section>;
}
