"use client";

import {useState} from "react";
import {Download} from "lucide-react";
import {Dialog} from "@/components/ui/dialog";
import {
  downloadExport,
  ExportDownloadError,
  type ExportFormat,
} from "@/lib/download";

export function ExportDialog({
  open,
  onOpenChange,
  title,
  dataset,
  endpoint,
  filters,
  filterSummary,
  estimatedRows,
  csrf,
  onUnauthorized,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  dataset: string;
  endpoint: string;
  filters: Record<string, unknown>;
  filterSummary: string[];
  estimatedRows: number;
  csrf: string;
  onUnauthorized: () => void;
}) {
  const [format, setFormat] = useState<ExportFormat>("csv");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const changeOpen = (value: boolean) => {
    if (busy) return;
    if (!value) {
      setFormat("csv");
      setError("");
    }
    onOpenChange(value);
  };

  const submit = async () => {
    setBusy(true);
    setError("");
    try {
      await downloadExport(endpoint, format, filters, csrf);
      setFormat("csv");
      setError("");
      onOpenChange(false);
    } catch (caught) {
      if (caught instanceof ExportDownloadError) {
        if (caught.status === 401) {
          onUnauthorized();
          return;
        }
        if (caught.status === 413) {
          setError("匹配数据超过导出限制，请缩小时间范围或增加筛选条件");
        } else if (caught.status === 429) {
          setError("当前有导出正在生成或请求过于频繁，请稍后重试");
        } else {
          setError("导出生成失败，当前筛选保持不变");
        }
      } else {
        setError("下载未完成，请检查网络后重试");
      }
    } finally {
      setBusy(false);
    }
  };

  return <Dialog
    open={open}
    onOpenChange={changeOpen}
    title={title}
    description={dataset}
    lockClose={busy}
  >
    <div className="export-dialog-body">
      <dl className="export-summary">
        <dt>当前筛选</dt>
        <dd>{filterSummary.length
          ? filterSummary.map((item) => <span key={item}>{item}</span>)
          : <span>全部数据</span>}</dd>
        <dt>预计行数</dt><dd><strong>{estimatedRows.toLocaleString()}</strong></dd>
      </dl>
      <fieldset className="export-format">
        <legend>数据格式</legend>
        <div className="segmented" aria-label="导出数据格式">
          <button type="button" className={format === "csv" ? "selected" : ""} onClick={() => setFormat("csv")}>CSV</button>
          <button type="button" className={format === "json" ? "selected" : ""} onClick={() => setFormat("json")}>JSON</button>
        </div>
      </fieldset>
      <div className="export-limits">最多 10,000 行 · 解压数据 50 MiB · ZIP 32 MiB</div>
      {error && <div className="form-error" role="alert">{error}</div>}
      <div className="modal-actions">
        <button className="secondary-button" disabled={busy} onClick={() => changeOpen(false)}>取消</button>
        <button className="primary-button" disabled={busy} onClick={() => void submit()}>
          <Download size={17} />{busy ? "正在生成..." : "下载 ZIP"}
        </button>
      </div>
    </div>
  </Dialog>;
}
