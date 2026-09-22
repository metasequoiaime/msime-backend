import { useQuery } from "@tanstack/react-query";
import { z } from "zod";
import { errorMessage } from "./api";
import { useAuth } from "./auth";
import { PageHeader } from "./shell";

const schema = z.object({ version: z.string(), server_time: z.string(), auth: z.boolean(), engine: z.boolean(), engine_resources: z.boolean(), services: z.record(z.string(), z.boolean()) });
const labels: Record<string, string> = { cloud: "云候选", chat: "对话模型", translation: "翻译", transcription: "语音转写", streaming_transcription: "流式转写" };
export function SystemStatus() {
  const { api } = useAuth();
  const query = useQuery({ queryKey: ["admin", "system"], queryFn: async ({ signal }) => schema.parse(await api("system", signal)) });
  return <><PageHeader page="system" refresh={() => void query.refetch()} busy={query.isFetching} />{query.isError && <p className="notice error" role="alert">{errorMessage(query.error)}</p>}{query.data && <section className="panel system-status"><dl className="user-facts"><dt>服务版本</dt><dd>{query.data.version}</dd><dt>检查时间</dt><dd>{new Date(query.data.server_time).toLocaleString("zh-CN")}</dd><dt>账号服务</dt><dd><State value={query.data.auth} /></dd><dt>输入法引擎</dt><dd><State value={query.data.engine} /></dd><dt>引擎资源</dt><dd><State value={query.data.engine_resources} /></dd></dl><h2>上游能力</h2><div className="service-grid">{Object.entries(query.data.services).map(([key, value]) => <div key={key}><span>{labels[key] || key}</span><State value={value} /></div>)}</div><p className="muted small">此页只展示能力是否配置，不返回上游地址、模型密钥或其他凭据。</p></section>}</>;
}
function State({ value }: { value: boolean }) { return <span className={value ? "badge" : "badge warning"}>{value ? "已启用" : "未启用"}</span>; }
