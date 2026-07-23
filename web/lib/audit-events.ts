import eventCatalog from "@/lib/audit-event-catalog.json";

export type AuditEventOption = {
  value: string;
  label: string;
};

export const auditEventOptions: readonly AuditEventOption[] = eventCatalog;

const eventLabels = new Map(
  auditEventOptions.map((event) => [event.value, event.label]),
);

const categoryLabels: Readonly<Record<string, string>> = {
  admin: "管理",
  auth: "认证",
  config: "配置",
  client: "客户端",
  security: "安全",
  system: "系统",
};

const outcomeLabels: Readonly<Record<string, string>> = {
  success: "成功",
  failure: "失败",
  denied: "拒绝",
  pending: "等待",
  unknown: "未知",
};

export function auditEventLabel(value: string) {
  return eventLabels.get(value) ?? "未知事件";
}

export function auditCategoryLabel(value: string) {
  return categoryLabels[value] ?? "未知分类";
}

export function auditOutcomeLabel(value: string) {
  return outcomeLabels[value] ?? "未知";
}
