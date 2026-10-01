import "./zod-config";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { AuthProvider } from "./auth";
import { router } from "./routes/router";
import { initAppearance } from "./shell/theme";
import { ConfirmProvider } from "./ui/confirm";
import { ToastProvider } from "./ui/toast";
import "./styles/app.css";

initAppearance();

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000, refetchOnWindowFocus: false }, mutations: { retry: false } } });
const root = document.getElementById("root");
if (!root) throw new Error("Missing application root");
createRoot(root).render(<StrictMode>
  <QueryClientProvider client={queryClient}>
    <AuthProvider>
      <ToastProvider>
        <ConfirmProvider>
          <RouterProvider router={router} />
        </ConfirmProvider>
      </ToastProvider>
    </AuthProvider>
  </QueryClientProvider>
</StrictMode>);
