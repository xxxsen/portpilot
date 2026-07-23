"use client";

import {Clock3, Mail, ShieldCheck} from "lucide-react";
import {FormEvent, useCallback, useEffect, useState} from "react";
import {api, ApiError} from "@/lib/api";
import type {KnockStatus} from "@/lib/types";

export default function KnockPage() {
  const [status, setStatus] = useState<KnockStatus | null>(null);
  const [email, setEmail] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [now, setNow] = useState(0);

  const loadStatus = useCallback(async () => {
    try {
      setStatus(await api<KnockStatus>("/api/knock/status"));
    } catch {
      setError("暂时无法读取访问授权状态");
    }
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => {
      setNow(Date.now());
      void loadStatus();
    }, 0);
    const statusTimer = window.setInterval(() => void loadStatus(), 30_000);
    const clockTimer = window.setInterval(() => setNow(Date.now()), 1_000);
    const focus = () => void loadStatus();
    window.addEventListener("focus", focus);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(statusTimer);
      window.clearInterval(clockTimer);
      window.removeEventListener("focus", focus);
    };
  }, [loadStatus]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError("");
    setMessage("");
    try {
      const result = await api<{message: string}>("/api/knock/requests", {
        method: "POST",
        body: JSON.stringify({email}),
      });
      setMessage(result.message + "。");
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 429) {
        setError("请求过于频繁，请稍后再试");
      } else if (caught instanceof ApiError && caught.status === 503) {
        setError("临时访问授权当前未启用");
      } else {
        setError("发送请求失败，请稍后重试");
      }
    } finally {
      setSubmitting(false);
    }
  };

  const remaining = status?.authorized
    ? Math.max(0, Math.ceil((status.expires_at - now) / 1000))
    : 0;

  return <main className="knock-page">
    <section className="knock-panel">
      <header className="knock-brand">
        <span className="brand-mark"><ShieldCheck size={20} /></span>
        <strong>PortPilot</strong>
      </header>
      <div className="knock-heading">
        <h1>临时访问授权</h1>
        <p>授权链接会发送到已登记的邮箱，并只对当前网络出口生效。</p>
      </div>
      <div className="knock-source">
        <span>当前 IP</span>
        <code>{status?.source_ip ?? "正在识别"}</code>
      </div>
      {status?.authorized ? <div className="knock-authorized">
        <ShieldCheck size={19} />
        <div>
          <strong>当前 IP 已获得访问权限</strong>
          <span><Clock3 size={14} />剩余 {formatCountdown(remaining)} · {new Date(status.expires_at).toLocaleString()} 到期</span>
        </div>
      </div> : <form className="knock-form" onSubmit={submit}>
        <label>授权邮箱
          <input
            type="email"
            autoComplete="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            placeholder="name@example.com"
            required
          />
        </label>
        <button className="primary-button" disabled={submitting || status?.enabled === false}>
          <Mail size={17} />{submitting ? "正在提交" : "发送访问邮件"}
        </button>
      </form>}
      {message && <div className="knock-message success">{message}</div>}
      {error && <div className="knock-message error">{error}</div>}
      {status?.enabled === false && <div className="knock-message neutral">临时访问授权当前未启用</div>}
    </section>
  </main>;
}

function formatCountdown(seconds: number) {
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  return `${minutes}:${String(remainder).padStart(2, "0")}`;
}
