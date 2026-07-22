"use client";

export type ExportFormat = "csv" | "json";

export class ExportDownloadError extends Error {
  constructor(
    public status: number,
    public code: string,
    public payload: Record<string, unknown>,
  ) {
    super(code);
  }
}

export async function downloadExport(
  endpoint: string,
  format: ExportFormat,
  filters: Record<string, unknown>,
  csrf: string,
): Promise<string> {
  const response = await fetch(`/backend${endpoint}`, {
    method: "POST",
    credentials: "include",
    cache: "no-store",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": csrf,
    },
    body: JSON.stringify({format, filters}),
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({})) as Record<string, unknown>;
    throw new ExportDownloadError(
      response.status,
      typeof payload.error === "string" ? payload.error : `HTTP ${response.status}`,
      payload,
    );
  }
  const contentType = response.headers.get("Content-Type")?.split(";")[0].trim();
  if (contentType !== "application/zip") {
    throw new ExportDownloadError(502, "invalid_export_response", {});
  }
  const filename = exportFilename(response.headers.get("Content-Disposition"));
  const blob = await response.blob();
  const objectURL = URL.createObjectURL(blob);
  try {
    const anchor = document.createElement("a");
    anchor.href = objectURL;
    anchor.download = filename;
    anchor.style.display = "none";
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
  } finally {
    URL.revokeObjectURL(objectURL);
  }
  return filename;
}

function exportFilename(disposition: string | null) {
  const match = disposition?.match(/(?:^|;)\s*filename="([^"]+)"(?:;|$)/i);
  const value = match?.[1] ?? "";
  if (/^portpilot-(network_connections|network_dimensions|audit_events)-\d{8}T\d{6}Z\.zip$/.test(value)) {
    return value;
  }
  return "portpilot-export.zip";
}
