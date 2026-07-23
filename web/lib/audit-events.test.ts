import {describe, expect, it} from "vitest";
import {
  auditCategoryLabel,
  auditEventLabel,
  auditEventOptions,
  auditOutcomeLabel,
} from "@/lib/audit-events";

describe("audit event presentation", () => {
  it("provides a unique user-facing name for every catalog event", () => {
    const values = new Set<string>();

    for (const event of auditEventOptions) {
      expect(event.value).toMatch(/^[a-z0-9_]+$/);
      expect(event.label.trim()).toBe(event.label);
      expect(event.label).not.toBe("");
      expect(event.label).not.toBe(event.value);
      expect(event.label).not.toContain("_");
      expect(values.has(event.value)).toBe(false);
      values.add(event.value);
    }
  });

  it("never exposes unknown internal codes as fallback labels", () => {
    expect(auditEventLabel("future_internal_event")).toBe("未知事件");
    expect(auditCategoryLabel("future_internal_category")).toBe("未知分类");
    expect(auditOutcomeLabel("future_internal_outcome")).toBe("未知");
  });
});
