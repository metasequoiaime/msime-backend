import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRootRoute, createRoute, createRouter, RouterProvider, Link } from "@tanstack/react-router";
import { AuthProvider } from "./auth";
import { Shell } from "./shell";
import { OverviewPage } from "./overview";
import { AdminMembers } from "./admin-members";
import { SystemStatus } from "./system-status";
import { ResourceList } from "./resource-list";
import { isListPage } from "./pages";
import "./tailwind.css";

function NotFound() { return <section className="panel"><h1>页面不存在</h1><Link to="/">返回数据总览</Link></section>; }
const rootRoute = createRootRoute({ component: Shell, notFoundComponent: NotFound });
const overviewRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: OverviewPage });
const listRoute = createRoute({ getParentRoute: () => rootRoute, path: "/$section", component: ListRoute });
function ListRoute() { const { section } = listRoute.useParams(); return section === "admins" ? <AdminMembers /> : section === "system" ? <SystemStatus /> : isListPage(section) ? <ResourceList key={section} section={section} /> : <NotFound />; }
const router = createRouter({ routeTree: rootRoute.addChildren([overviewRoute, listRoute]) });
declare module "@tanstack/react-router" { interface Register { router: typeof router } }
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000, refetchOnWindowFocus: false }, mutations: { retry: false } } });
const root = document.getElementById("root");
if (!root) throw new Error("Missing application root");
createRoot(root).render(<StrictMode><QueryClientProvider client={queryClient}><AuthProvider><RouterProvider router={router} /></AuthProvider></QueryClientProvider></StrictMode>);
