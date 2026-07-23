"use client";

import {CircleAlert, LoaderCircle, ShieldCheck} from "lucide-react";
import {useCallback, useEffect, useRef, useState} from "react";
import {api, ApiError} from "@/lib/api";
import type {KnockStatus} from "@/lib/types";

type ViewState = "verifying" | "success" | "invalid" | "mismatch" | "failure";

export default function KnockVerifyPage() {
  const started = useRef(false);
  const [view, setView] = useState<ViewState>("verifying");
  const [status, setStatus] = useState<KnockStatus | null>(null);
  const [now, setNow] = useState(0);

  const refreshStatus = useCallback(async () => {
    try {
      const current = await api<KnockStatus>("/api/knock/status");
      setStatus(current);
      if (current.authorized) setView("success");
    } catch {
      // Keep the verification result visible when a background refresh fails.
    }
  }, []);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    const fragment = new URLSearchParams(window.location.hash.slice(1));
    const token = fragment.get("token");
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
    if (!token) {
      window.setTimeout(() => setView("invalid"), 0);
      return;
    }
    void api<KnockStatus>("/api/knock/verify", {
      method: "POST",
      body: JSON.stringify({token}),
    }).then((result) => {
      setStatus({...result, enabled: true, authorized: true});
      setView("success");
    }).catch((caught) => {
      if (caught instanceof ApiError && caught.message === "source_ip_mismatch") {
        setView("mismatch");
      } else if (caught instanceof ApiError && [400, 401, 409].includes(caught.status)) {
        setView("invalid");
      } else {
        setView("failure");
      }
    });
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => setNow(Date.now()), 0);
    const statusTimer = window.setInterval(() => void refreshStatus(), 30_000);
    const clockTimer = window.setInterval(() => setNow(Date.now()), 1_000);
    window.addEventListener("focus", refreshStatus);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(statusTimer);
      window.clearInterval(clockTimer);
      window.removeEventListener("focus", refreshStatus);
    };
  }, [refreshStatus]);

  const remaining = status?.expires_at
    ? Math.max(0, Math.ceil((status.expires_at - now) / 1000))
    : 0;

  return <main className="knock-page">
    <section className="knock-panel verify">
      <header className="knock-brand">
        <span className="brand-mark"><ShieldCheck size={20} /></span>
        <strong>PortPilot</strong>
      </header>
      {view === "verifying" && <Result
        icon={<LoaderCircle className="spin" />}
        title="正在验证访问链接"
        detail="请保持此页面打开。"
      />}
      {view === "success" && <Result
        icon={<ShieldCheck />}
        title="临时访问授权已生效"
        detail={`${status?.source_ip ?? "当前 IP"} · 剩余 ${formatCountdown(remaining)}`}
        success
      />}
      {view === "invalid" && <Result
        icon={<CircleAlert />}
        title="链接无效或已过期"
        detail="请返回申请页面重新发送访问邮件。"
      />}
      {view === "mismatch" && <Result
        icon={<CircleAlert />}
        title="当前网络与申请时不同"
        detail="请回到申请访问邮件时使用的网络，再次打开原邮件链接。链接尚未被消费。"
      />}
      {view === "failure" && <Result
        icon={<CircleAlert />}
        title="暂时无法完成验证"
        detail="服务暂时不可用，请稍后重新打开邮件链接。"
      />}
      <a className="secondary-button knock-back" href="/knock">返回申请页面</a>
    </section>
  </main>;
}

function Result({
  icon,
  title,
  detail,
  success = false,
}: {
  icon: React.ReactNode;
  title: string;
  detail: string;
  success?: boolean;
}) {
  return <div className={`knock-result ${success ? "success" : ""}`}>
    <span>{icon}</span>
    <h1>{title}</h1>
    <p>{detail}</p>
  </div>;
}

function formatCountdown(seconds: number) {
  const minutes = Math.floor(seconds / 60);
  return `${minutes}:${String(seconds % 60).padStart(2, "0")}`;
}
