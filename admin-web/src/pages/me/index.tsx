import { useAuth } from "../../auth";
import { roleLabel } from "../../api/shell";
import { PageIntro } from "../../shell/page-intro";
import { useShell } from "../../shell/shell-data";
import { Button } from "../../ui/button";
import { Card, CardHeader } from "../../ui/card";
import { useConfirm } from "../../ui/confirm";

export default function MePage() {
  const { session, logout } = useAuth();
  const shell = useShell();
  const confirm = useConfirm();
  const me = shell.data?.me;
  const email = me?.email ?? session?.email ?? "";
  return <>
    <PageIntro page="me" />
    <Card className="max-w-[560px]">
      <CardHeader title={me?.name || email || "管理员"} sub={[me ? roleLabel(me.role) : "", email].filter(Boolean).join(" · ")} />
      <Button variant="danger-outline" onClick={async () => {
        if (await confirm({ title: "退出登录？", description: "当前浏览器的管理会话会被注销，需要重新登录。", okLabel: "退出" }) !== null) await logout();
      }}>退出登录</Button>
    </Card>
  </>;
}
